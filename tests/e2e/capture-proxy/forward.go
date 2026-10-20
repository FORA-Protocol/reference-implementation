package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"slices"
	"syscall"
	"time"
)

const (
	// dialTimeout bounds connecting to a party. A party that is down refuses at
	// once; this only cuts short a dial that hangs.
	dialTimeout = 10 * time.Second
	// idleUpstreamTimeout closes a pooled connection to a party that sat unused,
	// so a restarted party is not dialled on a connection it already dropped.
	idleUpstreamTimeout = 30 * time.Second
	// maxIdlePerParty bounds the pooled connections kept open to one party.
	maxIdlePerParty = 16
)

// forwardingHeaders are the headers httputil.ReverseProxy strips from the
// outbound request before Rewrite runs. No FORA party sends them, but a caller
// that did would expect the service to receive them as sent, so rewrite puts
// back what the caller sent.
var forwardingHeaders = []string{"Forwarded", "X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto"}

// errLoop refuses a dial to this container itself: a Host naming the proxy
// would otherwise forward to itself without end.
var errLoop = errors.New("capture-proxy: the request names this proxy itself")

// forwarder records each request that reaches one listening port and forwards it
// to the party its Host names. It forwards on the port it listens on, except on
// 443, which no party listens on: there the port comes from routes.
type forwarder struct {
	port func(host string) string
	shared
	proxy *httputil.ReverseProxy
}

// shared is what every listener's forwarder uses: where requests go, the record
// they are written to, the caller-name cache and the transport to the parties.
type shared struct {
	routes  routes
	book    *store
	callers *callerNames
	parties http.RoundTripper
}

// httpsPort is the port an https URL without one names.
const httpsPort = "443"

// newForwarder builds the forwarder for one listening port.
func newForwarder(port string, deps shared) *forwarder {
	portFor := func(string) string { return port }
	if port == httpsPort {
		portFor = deps.routes.tlsPort
	}
	f := &forwarder{port: portFor, shared: deps}
	f.proxy = &httputil.ReverseProxy{
		Rewrite:   f.rewrite,
		Transport: deps.parties,
		// A party that cannot be reached gets the caller no answer at all, the
		// way the caller would have seen it without the proxy.
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) { closeWithoutAnswer(w) },
		ErrorLog:     log.New(io.Discard, "", 0),
	}
	return f
}

// ServeHTTP records the request with its body bytes, then forwards it unchanged.
func (f *forwarder) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		closeWithoutAnswer(w)
		return
	}
	f.book.add(f.record(r.Context(), r, body))
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.ContentLength = int64(len(body))
	r.TransferEncoding = nil
	f.proxy.ServeHTTP(w, r)
}

// rewrite addresses the outbound request to the party its Host names, over plain
// http whichever leg it arrived on. The path, the query, the Host and every
// end-to-end header stay as the caller sent them.
func (f *forwarder) rewrite(pr *httputil.ProxyRequest) {
	host := hostOnly(pr.In.Host)
	pr.Out.URL.Scheme = "http"
	pr.Out.URL.Host = net.JoinHostPort(f.routes.upstreamHost(host), f.port(host))
	pr.Out.Host = pr.In.Host
	for _, name := range forwardingHeaders {
		if values, ok := pr.In.Header[name]; ok {
			pr.Out.Header[name] = slices.Clone(values)
		}
	}
}

// record describes the request as it arrived.
func (f *forwarder) record(ctx context.Context, r *http.Request, body []byte) Record {
	address := hostOnly(r.RemoteAddr)
	messageType, message, problem := decode(r.URL.Path, r.Header.Get("Content-Type"),
		r.Header.Get("Content-Encoding"), body)
	return Record{
		Scheme:        schemeOf(r),
		Target:        r.Host,
		CallerAddress: address,
		CallerNames:   f.callers.lookup(ctx, address),
		Method:        r.Method,
		Path:          r.RequestURI,
		Headers:       headerPairs(r.Header),
		Body:          body,
		MessageType:   messageType,
		Message:       message,
		DecodeProblem: problem,
	}
}

// schemeOf is the leg a request arrived on.
func schemeOf(r *http.Request) string {
	if r.TLS != nil {
		return "https"
	}
	return "http"
}

// hostOnly is the host of a host[:port] value.
func hostOnly(hostport string) string {
	if host, _, err := net.SplitHostPort(hostport); err == nil {
		return host
	}
	return hostport
}

// headerPairs lists every header value as a name and value pair, sorted by name.
// A field repeated on the wire keeps its values in the order they arrived.
func headerPairs(h http.Header) [][2]string {
	names := make([]string, 0, len(h))
	for name := range h {
		names = append(names, name)
	}
	slices.Sort(names)
	var pairs [][2]string
	for _, name := range names {
		for _, value := range h[name] {
			pairs = append(pairs, [2]string{name, value})
		}
	}
	return pairs
}

// closeWithoutAnswer drops the caller's connection without writing a response.
func closeWithoutAnswer(w http.ResponseWriter) {
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		w.WriteHeader(http.StatusBadGateway)
		return
	}
	conn, _, err := hijacker.Hijack()
	if err != nil {
		w.WriteHeader(http.StatusBadGateway)
		return
	}
	if tcp, isTCP := conn.(*net.TCPConn); isTCP {
		_ = tcp.SetLinger(0)
	}
	_ = conn.Close()
}

// upstreamTransport dials the parties by the names the network's DNS gives them.
// It never asks for compression and never decompresses, so an answer reaches
// the caller in the encoding the party chose.
func upstreamTransport(self map[string]bool) *http.Transport {
	dialer := &net.Dialer{
		Timeout: dialTimeout,
		Control: func(_, address string, _ syscall.RawConn) error {
			if self[hostOnly(address)] {
				return errLoop
			}
			return nil
		},
	}
	return &http.Transport{
		DialContext:         dialer.DialContext,
		DisableCompression:  true,
		MaxIdleConnsPerHost: maxIdlePerParty,
		IdleConnTimeout:     idleUpstreamTimeout,
	}
}

// ownAddresses is every address this container holds, loopback included.
func ownAddresses() (map[string]bool, error) {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil, err
	}
	self := make(map[string]bool, len(addrs))
	for _, addr := range addrs {
		if ipnet, ok := addr.(*net.IPNet); ok {
			self[ipnet.IP.String()] = true
		}
	}
	return self, nil
}
