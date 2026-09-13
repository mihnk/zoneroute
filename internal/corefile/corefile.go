// Package corefile inspects CoreDNS configuration text and reports which
// listeners it defines and whether the ZoneRoute integration is wired.
//
// It is a pure transformation from text to Go values: no Kubernetes access,
// no filesystem access. CoreDNS's own Caddyfile parser does the parsing;
// this package only classifies what the parser cannot see, namely import
// statements, which the parser would otherwise resolve against the local
// filesystem and consume.
package corefile

import (
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"

	"github.com/coredns/caddy/caddyfile"

	"github.com/mihnk/zoneroute/internal/dnsname"
)

const (
	// ManagedKey is the coredns-custom entry ZoneRoute owns. It is never
	// treated as an existing owner.
	ManagedKey = "zoneroute.server"

	// WiringImport is the top-level import the installation contract
	// requires in the Corefile.
	WiringImport = "custom/*.server"

	// CorefileSource is the Source value used for the main Corefile.
	CorefileSource = "Corefile"

	serverSuffix = ".server"
)

// Listener is a CoreDNS server-block identity. CoreDNS treats transport as
// part of that identity, so listeners with the same zone and port but
// different transports are distinct.
type Listener struct {
	Transport string // dns, tls, quic, grpc, https, https3, unix
	Zone      string // dnsname.Canonical form
	Port      int    // explicit, or the transport's default; 0 for unix
}

// Import is a top-level import statement ZoneRoute cannot resolve under
// the v0.1 integration contract.
type Import struct {
	Source  string // CorefileSource or a coredns-custom key
	Line    int
	Pattern string
}

// Result is what Inspect could see.
type Result struct {
	// Listeners maps every visible listener to the source it was found in:
	// CorefileSource or a coredns-custom key. When two sources define the
	// same listener the first one wins (Corefile, then keys in sorted order).
	Listeners map[Listener]string

	// Wired is true when the Corefile has a top-level `import custom/*.server`.
	Wired bool

	// UnresolvedImports lists top-level imports that are neither the wiring
	// import nor a snippet defined in the same source. Their contents may
	// define listeners ZoneRoute cannot see. Ordered by source, then line.
	UnresolvedImports []Import
}

// defaultPorts mirrors CoreDNS plugin/pkg/transport. unix sockets have no
// port; they are kept as a distinct transport so they never collide with
// DNS listeners.
var defaultPorts = map[string]int{
	"dns":    53,
	"tls":    853,
	"quic":   853,
	"grpc":   443,
	"https":  443,
	"https3": 443,
	"unix":   0,
}

// Inspect parses the Corefile and the coredns-custom server entries.
//
// Only keys ending in ".server" are parsed, because those are the files the
// wiring import includes; ManagedKey is skipped because ZoneRoute owns it.
// An error means the text is not something CoreDNS itself would accept.
func Inspect(corefile string, servers map[string]string) (Result, error) {
	res := Result{Listeners: map[Listener]string{}}

	wired, err := inspect(CorefileSource, corefile, &res)
	if err != nil {
		return Result{}, err
	}
	res.Wired = wired

	keys := make([]string, 0, len(servers))
	for k := range servers {
		if k != ManagedKey && strings.HasSuffix(k, serverSuffix) {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		if _, err := inspect(k, servers[k], &res); err != nil {
			return Result{}, err
		}
	}
	return res, nil
}

type importStmt struct {
	line    int
	pattern string
	depth   int
}

// inspect adds one source's listeners and unresolved imports to res and
// reports whether the source carries the wiring import at top level.
func inspect(source, text string, res *Result) (wired bool, err error) {
	imports, snippets := scanImports(source, text)

	// Remove import lines so the parser neither resolves them against the
	// local filesystem nor logs about missing files. Line numbers are kept.
	lines := strings.Split(text, "\n")
	for _, imp := range imports {
		lines[imp.line-1] = ""
	}

	blocks, err := caddyfile.Parse(source, strings.NewReader(strings.Join(lines, "\n")), nil)
	if err != nil {
		return false, fmt.Errorf("%s: %w", source, err)
	}
	for _, b := range blocks {
		for _, key := range b.Keys {
			l, err := parseKey(key)
			if err != nil {
				return false, fmt.Errorf("%s: %w", source, err)
			}
			if _, seen := res.Listeners[l]; !seen {
				res.Listeners[l] = source
			}
		}
	}

	for _, imp := range imports {
		if imp.depth > 0 || snippets[imp.pattern] {
			// Block-level imports cannot open server blocks; snippet
			// imports are resolved from the same text.
			continue
		}
		if source == CorefileSource && imp.pattern == WiringImport {
			wired = true
			continue
		}
		res.UnresolvedImports = append(res.UnresolvedImports, Import{
			Source: source, Line: imp.line, Pattern: imp.pattern,
		})
	}
	return wired, nil
}

// scanImports walks the token stream with CoreDNS's lexer and records every
// import statement with its brace depth, plus the snippet names defined at
// top level. The lexer already strips comments and quotes.
func scanImports(source, text string) ([]importStmt, map[string]bool) {
	var imports []importStmt
	snippets := map[string]bool{}

	d := caddyfile.NewDispenser(source, strings.NewReader(text))
	depth, prevLine := 0, 0
	for d.Next() {
		v, line := d.Val(), d.Line()
		lineStart := line != prevLine
		prevLine = line

		switch {
		case v == "{":
			depth++
		case v == "}":
			depth--
		case lineStart && depth == 0 && len(v) > 2 && v[0] == '(' && v[len(v)-1] == ')':
			snippets[v[1:len(v)-1]] = true
		case lineStart && v == "import":
			if d.NextArg() {
				imports = append(imports, importStmt{line: line, pattern: d.Val(), depth: depth})
			}
		}
	}
	return imports, snippets
}

// parseKey turns a server-block key such as "tls://Example.COM:1053" into a
// Listener, following CoreDNS: strip the transport scheme, split host and
// port, default the port per transport, canonicalize the zone.
func parseKey(key string) (Listener, error) {
	transport, host := "dns", key
	if scheme, rest, found := strings.Cut(key, "://"); found {
		transport, host = scheme, rest
		if _, ok := defaultPorts[transport]; !ok {
			return Listener{}, fmt.Errorf("server block %q: unknown transport %q", key, transport)
		}
	}
	port := defaultPorts[transport]

	switch h, p, err := net.SplitHostPort(host); {
	case err == nil:
		host = h
		n, err := strconv.Atoi(p)
		if err != nil || n < 1 || n > 65535 {
			return Listener{}, fmt.Errorf("server block %q: invalid port %q", key, p)
		}
		port = n
	case !strings.Contains(host, ":"):
		// No port given; keep the transport default.
	case strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]"):
		host = host[1 : len(host)-1] // bracketed IPv6 without a port
	default:
		return Listener{}, fmt.Errorf("server block %q: %w", key, err)
	}

	return Listener{Transport: transport, Zone: dnsname.Canonical(host), Port: port}, nil
}
