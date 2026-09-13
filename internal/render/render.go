// Package render turns accepted routes into the contents of the
// zoneroute.server entry that CoreDNS imports.
//
// It is a pure transformation from Go values to bytes: no Kubernetes types,
// no I/O. Callers pass only routes that a later decision layer has already
// accepted; Render neither filters nor validates acceptance.
package render

import (
	"bytes"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"

	"github.com/coredns/caddy/caddyfile"

	"github.com/mihnk/zoneroute/internal/dnsname"
)

// defaultPort is the upstream port that is omitted from output. It equals
// the API schema default by protocol convention, not by dependency: the two
// layers make the same DNS assumption independently.
const defaultPort int32 = 53

const header = "# Managed by ZoneRoute. Edits are overwritten.\n"

// Route is the minimal input the renderer needs for one accepted ZoneRoute.
type Route struct {
	Name       string
	Generation int64
	Zones      []string   // any case, with or without a trailing dot
	Upstreams  []Upstream // order is significant and preserved
}

// Upstream is one resolver. Address must already be a canonical IPv4 or
// IPv6 literal; the renderer writes it as given.
type Upstream struct {
	Address string
	// Port 0 is treated as 53. This exists only for Go values built by
	// hand: objects that went through the API server always carry an
	// explicit port because the schema defaults it.
	Port int32
}

// Render produces the full zoneroute.server text for routes.
//
// Output is byte-for-byte deterministic for the same set of routes,
// regardless of input order: blocks are sorted by Name and zones within a
// block are canonicalized and sorted. Upstream order is preserved exactly.
//
// The result is parsed with CoreDNS's own Caddyfile parser before it is
// returned. An error therefore means a rendering bug, never a user error.
func Render(routes []Route) ([]byte, error) {
	sorted := make([]Route, len(routes))
	copy(sorted, routes)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })

	var b bytes.Buffer
	b.WriteString(header)
	for _, r := range sorted {
		b.WriteString("\n")
		fmt.Fprintf(&b, "# zoneroute: %s (generation %d)\n", r.Name, r.Generation)
		b.WriteString(strings.Join(zoneKeys(r.Zones), " "))
		b.WriteString(" {\n    forward .")
		for _, u := range r.Upstreams {
			b.WriteByte(' ')
			b.WriteString(upstreamAddr(u))
		}
		b.WriteString(" {\n        policy sequential\n    }\n}\n")
	}

	out := b.Bytes()
	if err := validate(out); err != nil {
		return nil, err
	}
	return out, nil
}

// validate checks that the generated fragment is parseable by CoreDNS's
// Caddyfile parser. It is not a semantic validator of user input: zone
// names and addresses are validated by the CRD schema before they reach
// the renderer, and the parser itself accepts almost any key text. What it
// catches is a structural rendering bug, such as a block that is opened and
// never closed.
func validate(fragment []byte) error {
	if _, err := caddyfile.Parse("zoneroute.server", bytes.NewReader(fragment), nil); err != nil {
		return fmt.Errorf("rendered fragment does not parse: %w", err)
	}
	return nil
}

// zoneKeys canonicalizes, sorts, and strips the trailing dot: CoreDNS
// accepts both forms, and the dotless one is the Corefile convention.
func zoneKeys(zones []string) []string {
	keys := make([]string, len(zones))
	for i, z := range zones {
		keys[i] = dnsname.Canonical(z)
	}
	sort.Strings(keys)
	for i, k := range keys {
		keys[i] = strings.TrimSuffix(k, ".")
	}
	return keys
}

// upstreamAddr formats one forward destination. The default port is
// omitted; CoreDNS adds it itself, for IPv6 literals too. A non-default
// port uses host:port, which for IPv6 means brackets.
func upstreamAddr(u Upstream) string {
	port := u.Port
	if port == 0 {
		port = defaultPort
	}
	if port == defaultPort {
		return u.Address
	}
	return net.JoinHostPort(u.Address, strconv.Itoa(int(port)))
}
