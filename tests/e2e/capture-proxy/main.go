// Command capture-proxy records every request the e2e stack's services send the
// Broker and the Exchanges, with its bytes, and forwards each one unchanged
// (ADR-028 D8 H12).
//
// Several obligations are about what one party sends another: the fan-out query
// the Broker sends each Exchange, the sub-request it relays a purchase in, the
// Register the identity service sends an Exchange as the agent. None of that is
// in any answer a test can read, so the stack records it on the way.
//
// How a request reaches it. The services that call the Broker and the Exchanges
// resolve those parties' names to this container through their hosts file
// (compose extra_hosts), so they send every such request here with the Host they
// meant. This proxy resolves the same name through the network's own DNS, which
// still names the real service, and forwards the request there. No service is
// configured with a different address and none knows the proxy exists: the
// request a service sends, the URL its signature covers and the Host it names
// are the ones it would send without the proxy.
//
// What it changes. Nothing a party reads: the method, the request target, the
// Host, every end-to-end header and the body bytes reach the service exactly as
// sent, and the answer reaches the caller exactly as the service gave it. Two
// things differ at the HTTP framing level, as at any HTTP intermediary: the
// hop-by-hop headers (RFC 9110 section 7.6.1) belong to each connection, and a
// chunked request body goes on with a Content-Length instead, because the proxy
// reads the body whole to record it. When the service cannot be reached, the
// caller's connection is closed with no answer, so the caller sees a transport
// failure as it would without the proxy, never an answer the proxy made up.
//
// It is also the stack's one TLS endpoint. The Web Bot Auth profile serves every
// key directory over https and names every signer by an https origin, and every
// service here speaks plain http. Every port the proxy listens on answers https
// as well as plain http, under a certificate from the local CA the stack mints
// at start, and forwards each request over plain http: on the port it arrived
// on, or, for 443, which no party listens on, on the port the CAPTURE_TLS_PORTS
// table names for its host (80 when it names none). The names that reach it are the ones the
// services' hosts files point here and the zones the wildcard-DNS sidecar points
// here; CAPTURE_ZONES names the service behind each such zone, because the
// network's DNS cannot. A request is recorded the same way on either leg, with
// the leg it arrived on.
//
// What it records, for each request: the Host it was sent to, the caller's
// address and the names the network's DNS gives that address, the method, the
// request target, every header, the body bytes, and, for a FORA RPC, the request
// message decoded from those bytes. The harness reads the record on the control
// listener (see control.go). The record lives in this process's memory, bounded,
// oldest first out.
package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/clock"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/runhttp"
)

const (
	controlAddrEnv     = "CAPTURE_CONTROL_ADDR"
	defaultControlAddr = ":9090"
	portsEnv           = "CAPTURE_PORTS"
	// The PEM certificate chain and key every port answers https with, and the
	// two routing tables described in routes.go.
	tlsCertEnv  = "CAPTURE_TLS_CERT"
	tlsKeyEnv   = "CAPTURE_TLS_KEY"
	zonesEnv    = "CAPTURE_ZONES"
	tlsPortsEnv = "CAPTURE_TLS_PORTS"
)

func main() {
	runhttp.MaybeHealthcheck(controlAddrEnv, defaultControlAddr)
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	specs, err := build(clock.System{}, settingsFromEnv())
	if err != nil {
		logger.Error("capture-proxy.exit", "err", err)
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err = runhttp.ServeGroup(ctx, logger, specs...)
	stop() // explicit (not deferred) so it runs before a non-zero os.Exit
	if err != nil {
		logger.Error("capture-proxy.exit", "err", err)
		os.Exit(1)
	}
}

// settings is the proxy's configuration, read from the environment once.
type settings struct {
	ports       string
	controlAddr string
	tlsCert     string
	tlsKey      string
	zones       string
	tlsPorts    string
}

func settingsFromEnv() settings {
	return settings{
		ports:       os.Getenv(portsEnv),
		controlAddr: runhttp.EnvOr(controlAddrEnv, defaultControlAddr),
		tlsCert:     os.Getenv(tlsCertEnv),
		tlsKey:      os.Getenv(tlsKeyEnv),
		zones:       os.Getenv(zonesEnv),
		tlsPorts:    os.Getenv(tlsPortsEnv),
	}
}

// build assembles the control listener and one forwarding listener per port,
// each answering plain http and https (see listen.go).
func build(clk clock.Clock, cfg settings) ([]runhttp.ServerSpec, error) {
	ports, err := parsePorts(cfg.ports)
	if err != nil {
		return nil, err
	}
	table, err := parseRoutes(cfg.zones, cfg.tlsPorts)
	if err != nil {
		return nil, err
	}
	tlsConfig, err := loadTLS(cfg.tlsCert, cfg.tlsKey)
	if err != nil {
		return nil, err
	}
	self, err := ownAddresses()
	if err != nil {
		return nil, err
	}
	book := newStore(clk, maxRecords, maxRecordBytes)
	deps := shared{routes: table, book: book, callers: newCallerNames(clk), parties: upstreamTransport(self)}
	specs := []runhttp.ServerSpec{{Name: "capture-control", Addr: cfg.controlAddr, Handler: controlHandler(book)}}
	for _, port := range ports {
		ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", ":"+port)
		if err != nil {
			closeAll(specs)
			return nil, fmt.Errorf("listen on %s: %w", port, err)
		}
		specs = append(specs, runhttp.ServerSpec{
			Name:     "capture-" + port,
			Addr:     ":" + port,
			Handler:  newForwarder(port, deps),
			Listener: newDualListener(ln, tlsConfig, clk),
		})
	}
	return specs, nil
}

// closeAll releases the listeners already bound when a later one fails.
func closeAll(specs []runhttp.ServerSpec) {
	for _, spec := range specs {
		if spec.Listener != nil {
			_ = spec.Listener.Close()
		}
	}
}

// loadTLS reads the certificate the TLS listener answers with. It offers
// HTTP/1.1 only, so a party that cannot be reached is answered the way the
// plain-http legs answer it: the connection closed with no response.
func loadTLS(certFile, keyFile string) (*tls.Config, error) {
	if certFile == "" || keyFile == "" {
		return nil, errors.New(tlsCertEnv + " and " + tlsKeyEnv + " are required: every port answers https too")
	}
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("load the TLS certificate: %w", err)
	}
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
		NextProtos:   []string{"http/1.1"},
	}, nil
}

// parsePorts reads the comma-separated list of ports the parties listen on.
func parsePorts(list string) ([]string, error) {
	var ports []string
	for _, raw := range strings.Split(list, ",") {
		port := strings.TrimSpace(raw)
		if port == "" {
			continue
		}
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return nil, fmt.Errorf("%s: %q is not a port", portsEnv, port)
		}
		ports = append(ports, port)
	}
	if len(ports) == 0 {
		return nil, errors.New(portsEnv + " names no port to forward")
	}
	return ports, nil
}
