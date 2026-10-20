package main

import (
	"fmt"
	"strconv"
	"strings"
)

// defaultTLSPort is the port a request that arrived over TLS is forwarded to
// when the TLS port table does not name its host: the port every key-directory
// host and publisher edge in the stack serves plain http on.
const defaultTLSPort = "80"

// routes decides where a request goes, from the host its caller named.
//
// Two tables, both read from the environment:
//
//   - zones maps a DNS zone to the service that answers for every name in it.
//     The stack's wildcard-DNS sidecar points a whole zone (the identity
//     service's agents, the suite's per-test agents and sites) at this proxy, so
//     the network's DNS cannot name the service behind such a name: it would
//     answer with this proxy, or not at all. A name in no zone is forwarded to
//     itself, resolved through the network's DNS.
//   - tlsPorts maps a host to the plain-http port its service listens on, for a
//     request that arrived on the TLS listener. A plain-http request keeps the
//     port it arrived on; an https request arrives on 443, which no service
//     listens on, so the port has to come from somewhere.
type routes struct {
	zones    map[string]string
	tlsPorts map[string]string
}

// parseRoutes reads both tables. zoneList is "zone=service,..." and portList is
// "host=port,...". Either may be empty.
func parseRoutes(zoneList, portList string) (routes, error) {
	zones, err := parsePairs(zonesEnv, zoneList, func(string) error { return nil })
	if err != nil {
		return routes{}, err
	}
	ports, err := parsePairs(tlsPortsEnv, portList, func(port string) error {
		n, convErr := strconv.Atoi(port)
		if convErr != nil || n < 1 || n > 65535 {
			return fmt.Errorf("%q is not a port", port)
		}
		return nil
	})
	if err != nil {
		return routes{}, err
	}
	return routes{zones: zones, tlsPorts: ports}, nil
}

// parsePairs reads "key=value,..." into a map, lowercasing the keys.
func parsePairs(env, list string, check func(string) error) (map[string]string, error) {
	pairs := map[string]string{}
	for _, raw := range strings.Split(list, ",") {
		item := strings.TrimSpace(raw)
		if item == "" {
			continue
		}
		key, value, ok := strings.Cut(item, "=")
		key, value = strings.ToLower(strings.TrimSpace(key)), strings.TrimSpace(value)
		if !ok || key == "" || value == "" {
			return nil, fmt.Errorf("%s: %q is not name=value", env, item)
		}
		if err := check(value); err != nil {
			return nil, fmt.Errorf("%s: %s: %w", env, key, err)
		}
		pairs[key] = value
	}
	return pairs, nil
}

// upstreamHost is the name the request is forwarded to: the service its zone
// names, or the host itself.
func (r routes) upstreamHost(host string) string {
	host = strings.ToLower(host)
	for zone, service := range r.zones {
		if host == zone || strings.HasSuffix(host, "."+zone) {
			return service
		}
	}
	return host
}

// tlsPort is the plain-http port an https request for host is forwarded to.
func (r routes) tlsPort(host string) string {
	if port, ok := r.tlsPorts[strings.ToLower(host)]; ok {
		return port
	}
	return defaultTLSPort
}
