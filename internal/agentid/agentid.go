// Package agentid turns a verified signer's Signature-Agent directory origin into
// that signer's stable identity (FromDirectory). The origin itself is read off
// the request by the protocol SDK's verifier, which accepts it only as an https
// origin and carries it on the VerifiedRequest; this package reduces it to the
// identity everything keys on.
//
// After the WBA split the RFC 9421 keyid is only a key thumbprint — proof of
// possession, not identity — so the identity is the directory the signature
// commits to. That value arrives as a URL, and using it verbatim would make one
// party several agents with several registrations, key pins and billing refs.
// The identity is therefore the host — with its port, which a deployment may
// distinguish by — reduced to one canonical spelling.
//
// Dropping the scheme never makes a plaintext directory acceptable. A
// Signature-Agent member that is not an https origin is refused by the SDK
// verifier before any identity is derived, and every key directory this
// repository reads is read over https from the host's own authority (the SDK's
// fetcher, through forawellknown.NewDirectoryFetcher and internal/agentkeys),
// whatever scheme a caller wrote.
package agentid

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"unicode/utf8"

	"golang.org/x/net/idna"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/forawellknown"
)

// FromDirectory returns the agent identity for a Signature-Agent directory
// origin: its host including any port, with the scheme removed. It accepts the
// three shapes a directory origin arrives in — a full origin
// ("https://agent.example"), a bare host ("agent.example"), and a host:port
// ("identity:8080") — and is idempotent, so re-normalizing an already-normalized
// identity is safe.
//
// It rejects anything that does not name a host. That rejection is the reason
// this returns an error rather than a best-effort string: the result becomes a
// primary key in the agents table and the host an outbound directory fetch is
// aimed at, so a value that is not a host must not reach either. In particular it
// refuses the two Signature-Agent forms FORA does not support — the spec's
// sf-dictionary (`agent2="https://a.example"`) and its data: URI, which inlines a
// whole key directory into the header — because neither parses to a host.
//
// Dropping the scheme is necessary but not sufficient, so the host is reduced to
// a canonical spelling as well — see canonicalHost. DNS does not distinguish
// case, the trailing root dot, or a port the scheme already implies, so without
// that folding one party serving one directory still registers as
// "agent.example", "Agent.Example", "agent.example." and "agent.example:443":
// four rows, four key pins, four billing refs, and a deactivated agent one
// keystroke away from a fresh account that defaults to active.
//
// agentreg derives the host it anchors a registration to through this same
// function, so the value the anchoring check compares and the value stored as the
// identity are one derivation rather than two that have to be kept in agreement.
func FromDirectory(directory string) (string, error) {
	host, err := forawellknown.HostOf(directory)
	if err != nil {
		return "", notAHost(directory, err)
	}
	if host == "" {
		// HostOf answers ("", nil) for a reference whose authority parses empty
		// ("//agent.example", "/foo"). Refusing it here is the only thing standing
		// between such a value and the agents table, because agentreg's anchoring
		// check compares two empty hosts and passes vacuously.
		return "", notAHost(directory, errors.New("empty authority"))
	}
	canonical, err := canonicalHost(host)
	if err != nil {
		return "", notAHost(directory, err)
	}
	return canonical, nil
}

// ErrNotAHost is the sentinel every refusal in this package wraps: the value
// cannot be an identity. It is exported by the package that owns the derivation
// so callers classify one condition rather than each coining its own — two
// packages did, and only one of them was ever classified, so the caller the other
// existed to catch received a 500 for what is a caller fault.
var ErrNotAHost = errors.New("agentid: value does not name a directory host")

// notAHost renders every refusal in this package through one template. The three
// call sites are one failure class — "this value cannot be an identity" — and a
// caller that matches on the message should not have to know which check fired.
func notAHost(directory string, cause error) error {
	return fmt.Errorf("%w: %q: %w", ErrNotAHost, directory, cause)
}

// hostChars is the character set a canonical host label may draw from, after
// lowercasing and A-label mapping. Anything else — ";", "/", "@", "%", a space —
// means url.Parse carried something into the authority that is not a host name,
// and that value must not become a storage key or a fetch target.
const hostChars = "abcdefghijklmnopqrstuvwxyz0123456789-_"

// canonicalHost reduces a host to the single spelling that names it, so one agent
// cannot hold several identities by varying how it writes its own host.
//
// Three foldings, each removing a distinction DNS itself does not draw:
//
//	case          "Agent.Example" and "agent.example" are the same name.
//	trailing dot  the root label is optional in a reference.
//	port          the number is what names the port, not its spelling — see
//	              canonicalPort. :80 and :443 fold away entirely; any other port
//	              is KEPT, because a compose stack really does serve distinct
//	              agents as "identity:8080" and folding that away would collapse
//	              agents rather than aliases.
//	name          a DNS name is folded to its A-label form; a bracketed IP
//	              literal to the address's own canonical text — see canonicalName.
func canonicalHost(host string) (string, error) {
	rawName, rawPort := splitHostPort(host)
	port, err := canonicalPort(rawPort)
	if err != nil {
		return "", err
	}
	name, err := canonicalName(strings.ToLower(strings.TrimSuffix(rawName, ".")))
	if err != nil {
		return "", err
	}
	if port == "" {
		return name, nil
	}
	return name + ":" + port, nil
}

// canonicalName reduces the name half of an authority — everything before the
// port — to the single spelling that names it. The two kinds of name fold by
// different rules, and neither rule folds the other's spellings.
//
// A non-ASCII DNS name is mapped to its A-label form so "пример.рф" and
// "xn--e1afmkfd.xn--p1ai" converge on what DNS resolves. The mapping is skipped
// for a name that is already ASCII — which is every host in a FORA deployment
// today — so idna's strict Lookup profile cannot begin refusing a host that works
// now, and the result is ASCII either way, which is what keeps FromDirectory
// idempotent.
func canonicalName(name string) (string, error) {
	if inner, ok := strings.CutPrefix(name, "["); ok {
		return canonicalIPLiteral(inner)
	}
	if !isASCII(name) {
		ascii, err := idna.Lookup.ToASCII(name)
		if err != nil {
			return "", fmt.Errorf("%q has no A-label form: %w", name, err)
		}
		name = ascii
	}
	if endsInNumericLabel(name) {
		return canonicalIPv4(name)
	}
	if err := checkHostName(name); err != nil {
		return "", err
	}
	return name, nil
}

// endsInNumericLabel reports whether name's last label is all digits, which means
// it cannot be a DNS name. RFC 1123 §2.1 requires the highest-level label to be
// non-numeric precisely so that one reference can never be read as a host name by
// one party and as an IPv4 address by another. It is what lets this package route
// "127.0.0.1" to the address rules and "agent.example" to the name rules without
// guessing.
func endsInNumericLabel(name string) bool {
	last := name[strings.LastIndex(name, ".")+1:]
	if last == "" {
		return false
	}
	return strings.IndexFunc(last, func(r rune) bool { return r < '0' || r > '9' }) < 0
}

// canonicalIPv4 canonicalizes a dotted-quad. checkHostName would otherwise pass
// one straight through — digits and dots are ordinary host characters — so
// "127.0.0.1" and "127.000.000.001" were two identities for one address, the same
// spelling-versus-value split the port fold and the IPv6 literal had.
//
// A padded octet is REFUSED rather than folded, which is the difference from the
// other two. "010.0.0.1" is 10.0.0.1 read as decimal and 8.0.0.1 read as octal,
// and the inet_aton family really does read it as octal — so there is no one
// address the padded text names, and folding it would mint an identity that
// denotes a different machine at dial time than it did at storage time. Refusing
// is the only answer that cannot be wrong; netip.ParseAddr rejects leading zeros
// for exactly this reason, so this function inherits the decision rather than
// making it. That also means a dotted-quad that parses is ALREADY canonical: what
// this adds is the refusal of the spellings that are not.
//
// A name that ends in digits but is no address at all ("12345", "1.2.3.4.5",
// "999.1.1.1") is refused here too. It could never have resolved, so it could only
// ever have been a second identity for a host whose directory lives elsewhere.
func canonicalIPv4(name string) (string, error) {
	addr, err := netip.ParseAddr(name)
	if err != nil {
		return "", fmt.Errorf("%q ends in a numeric label, so it can only be an IPv4 address: %w", name, err)
	}
	if !addr.Is4() {
		// Unreachable through url.Parse, which reads the colons of an unbracketed
		// IPv6 address as a port delimiter and refuses it first. Kept because this
		// function's contract is "the result is an IPv4 address", and that must not
		// depend on which caller's parser ran ahead of it.
		return "", fmt.Errorf("%q is an IPv6 address, which a URL authority must bracket", name)
	}
	return addr.String(), nil
}

// canonicalIPLiteral reduces a bracketed IP literal to the one spelling that
// names the address, given the text between the brackets.
//
// Lowercasing the hex digits is NOT the whole of an IPv6 address's canonical
// form, which is what treating the literal as opaque assumed. "[::1]",
// "[0:0:0:0:0:0:0:1]" and "[::0001]" are one address written three ways, and no
// resolver is involved to collapse them the way DNS collapses a name's spellings
// — so left as text they were three identities for one machine, each taking its
// own row, key pin and billing ref. netip.ParseAddr + String is the RFC 5952
// canonical text, and it folds the IPv4-mapped forms too ("[::ffff:7f00:1]" and
// "[::ffff:127.0.0.1]" are the same address).
//
// A zone id is refused rather than canonicalized. "%eth0" names an interface on
// one machine, so the same string denotes different things on two hosts and
// denotes nothing at all once stored — it cannot be a shared identity. Refusing
// it also removes the one value FromDirectory was not idempotent for: the zone's
// "%" arrives percent-encoded, survives one pass decoded, and then fails to parse
// on the second because a bare "%" is not legal in a URL authority.
func canonicalIPLiteral(inner string) (string, error) {
	inner, ok := strings.CutSuffix(inner, "]")
	if !ok {
		return "", fmt.Errorf("%q is missing its closing bracket", "["+inner)
	}
	addr, err := netip.ParseAddr(inner)
	if err != nil {
		return "", fmt.Errorf("%q is not an IP address: %w", inner, err)
	}
	if addr.Zone() != "" {
		return "", fmt.Errorf("%q carries a zone id, which names an interface rather than a host", inner)
	}
	return "[" + addr.String() + "]", nil
}

// splitHostPort separates an authority into host and port, preserving the
// brackets net.SplitHostPort strips from an IPv6 literal so the two forms stay
// distinguishable downstream. A ref carrying no port is returned whole.
func splitHostPort(host string) (name, port string) {
	h, p, err := net.SplitHostPort(host)
	if err != nil {
		return host, ""
	}
	if strings.Contains(h, ":") {
		return "[" + h + "]", p
	}
	return h, p
}

// canonicalPort reduces a port to the single spelling that names it, and returns
// "" for a port the scheme already implies.
//
// The number is the port; its spelling is not. ":443", ":0443" and ":00443" all
// dial 443 — net.LookupPort and every TCP stack read them as one — so a fold that
// compared the TEXT left one endpoint able to mint unbounded distinct identities
// for itself by padding zeros, which is precisely the duplicate-row, duplicate-key-pin,
// duplicate-billing-ref outcome FromDirectory exists to prevent. Both halves matter:
// dropping the defaults numerically fixes ":0443", and RE-EMITTING from the parsed
// integer is what folds ":08080" onto ":8080" for the non-default ports that survive.
//
// A port outside 1–65535 is refused rather than kept. Such a value can never be
// dialed, so it cannot name a real directory — it can only be a second identity for
// a host whose real directory lives elsewhere.
//
// The non-numeric refusal is defence in depth: url.Parse inside forawellknown.HostOf
// already rejects "a.example:foo" before this runs. It stays because this function's
// contract is "the result is a port", and that must not depend on which caller's
// parser ran first.
func canonicalPort(port string) (string, error) {
	if port == "" {
		return "", nil
	}
	n, err := strconv.Atoi(port)
	switch {
	case errors.Is(err, strconv.ErrRange):
		// All digits, but too many of them to be a port.
		return "", fmt.Errorf("port %q is outside 1-65535", port)
	case err != nil:
		return "", fmt.Errorf("port %q is not numeric", port)
	case n < 1 || n > 65535:
		return "", fmt.Errorf("port %q is outside 1-65535", port)
	case n == 80 || n == 443:
		// Exactly the ports the two schemes FORA deploys under already imply.
		return "", nil
	}
	return strconv.Itoa(n), nil
}

// checkHostName refuses an authority that is not a host name. It runs after
// lowercasing and A-label mapping, so name is ASCII and can be indexed by byte.
func checkHostName(name string) error {
	if name == "" {
		return errors.New("empty host")
	}
	for _, label := range strings.Split(name, ".") {
		if label == "" {
			return fmt.Errorf("%q has an empty label", name)
		}
		if i := strings.IndexFunc(label, func(r rune) bool {
			return !strings.ContainsRune(hostChars, r)
		}); i >= 0 {
			return fmt.Errorf("%q contains %q, which a host name may not carry", name, label[i:i+1])
		}
	}
	return nil
}

func isASCII(s string) bool {
	for i := range len(s) {
		if s[i] >= utf8.RuneSelf {
			return false
		}
	}
	return true
}
