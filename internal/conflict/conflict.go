// Package conflict decides which ZoneRoutes are accepted when several claim
// the same zone, when a zone is already served by visible CoreDNS
// configuration, and rejects routes that claim the cluster domain.
//
// It is a pure transformation from Go values to Go values: no Kubernetes
// types, no I/O. It returns structured decisions only; condition reasons
// and messages are the controller's concern.
package conflict

import (
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/mihnk/zoneroute/internal/dnsname"
)

// The listener identity of every block ZoneRoute renders. The renderer
// writes neither a transport scheme nor a port, so the block is served on
// CoreDNS's plain-DNS default.
//
// ZoneRoute v0.1 assumes CoreDNS listens on the standard port 53. A CoreDNS
// process started with a non-default -dns.port is outside the v0.1
// integration contract; the controller deliberately does not inspect the
// Deployment or process arguments to infer the real value.
const (
	listenerTransport = "dns"
	listenerPort      = 53
)

// Route is the minimal input for one ZoneRoute.
type Route struct {
	Name    string
	Created time.Time // metadata.creationTimestamp; second resolution from the API
	Zones   []string  // any case, with or without a trailing dot
}

// Existing is a listener already defined by visible CoreDNS configuration,
// as reported by internal/corefile. Transport must be the normalized
// transport name; it is compared exactly and no default is applied.
type Existing struct {
	Transport string // dns, tls, quic, grpc, https, https3, unix
	Zone      string // any form; canonicalized here
	Port      int
	Source    string // "Corefile" or a coredns-custom key such as "foo.server"
}

// OwnedByCoreDNS records that a route's zone is already served by CoreDNS
// on the listener ZoneRoute would use.
type OwnedByCoreDNS struct {
	Zone   string // canonical form
	Port   int    // the ZoneRoute listener port
	Source string // where the existing listener was found
}

// ZoneConflict records that a zone is already owned by an accepted route.
type ZoneConflict struct {
	Zone   string // canonical form
	Winner string // name of the accepted route that owns Zone
}

// Decision is the outcome for one route. Exactly one state holds, in this
// precedence: Reserved, then Owned, then Conflicts, then Accepted.
type Decision struct {
	Name      string
	Accepted  bool
	Reserved  []string         // canonical zones inside the cluster domain, sorted
	Owned     []OwnedByCoreDNS // sorted by Zone
	Conflicts []ZoneConflict   // sorted by Zone
}

// Resolve decides acceptance for every route.
//
// Routes claiming the cluster domain or any of its descendants are rejected
// outright. Routes claiming a zone that visible CoreDNS configuration
// already serves on ZoneRoute's listener identity ("dns", zone, 53) are
// rejected next; a different port or transport is a different listener and
// does not conflict, and neither do parent or child zones. Rejected routes
// own nothing.
//
// The remaining routes are processed in (Created, Name) order: a route
// whose zones are all unowned is accepted and takes ownership of them; a
// route with any zone already owned by an earlier accepted route is
// rejected and owns nothing. Ownership therefore only ever points to an
// accepted route, and the oldest claim wins, with the name as a total
// tie-break for equal timestamps.
//
// clusterDomain is a required safety input: an empty value is an error, so
// a broken controller configuration can never let the cluster domain be
// claimed. Output is sorted by Name and is identical for any permutation
// of routes or existing. Inputs are not mutated.
func Resolve(routes []Route, existing []Existing, clusterDomain string) ([]Decision, error) {
	if strings.TrimSpace(clusterDomain) == "" {
		return nil, errors.New("conflict: cluster domain must not be empty")
	}
	domain := dnsname.Canonical(clusterDomain)
	descendant := "." + domain

	// Only listeners on ZoneRoute's own identity can collide with a route.
	// When several entries describe the same listener, the reported Source
	// is the bytewise-smallest one; Source is diagnostic metadata and never
	// influences acceptance.
	served := make(map[string]OwnedByCoreDNS)
	for _, e := range existing {
		if e.Transport != listenerTransport || e.Port != listenerPort {
			continue
		}
		zone := dnsname.Canonical(e.Zone)
		if cur, seen := served[zone]; !seen || e.Source < cur.Source {
			served[zone] = OwnedByCoreDNS{Zone: zone, Port: listenerPort, Source: e.Source}
		}
	}

	type candidate struct {
		name    string
		created time.Time
		zones   []string
	}
	decisions := make([]Decision, 0, len(routes))
	candidates := make([]candidate, 0, len(routes))

	for _, r := range routes {
		zones := canonicalZones(r.Zones)

		var reserved []string
		for _, z := range zones {
			if z == domain || strings.HasSuffix(z, descendant) {
				reserved = append(reserved, z)
			}
		}
		if len(reserved) > 0 {
			decisions = append(decisions, Decision{Name: r.Name, Reserved: reserved})
			continue
		}

		var owned []OwnedByCoreDNS
		for _, z := range zones {
			if o, ok := served[z]; ok {
				owned = append(owned, o)
			}
		}
		if len(owned) > 0 {
			decisions = append(decisions, Decision{Name: r.Name, Owned: owned})
			continue
		}

		candidates = append(candidates, candidate{name: r.Name, created: r.Created, zones: zones})
	}

	sort.SliceStable(candidates, func(i, j int) bool {
		if !candidates[i].created.Equal(candidates[j].created) {
			return candidates[i].created.Before(candidates[j].created)
		}
		return candidates[i].name < candidates[j].name
	})

	owner := make(map[string]string)
	for _, c := range candidates {
		var conflicts []ZoneConflict
		for _, z := range c.zones {
			if w, owned := owner[z]; owned {
				conflicts = append(conflicts, ZoneConflict{Zone: z, Winner: w})
			}
		}
		if len(conflicts) > 0 {
			decisions = append(decisions, Decision{Name: c.name, Conflicts: conflicts})
			continue
		}
		for _, z := range c.zones {
			owner[z] = c.name
		}
		decisions = append(decisions, Decision{Name: c.name, Accepted: true})
	}

	sort.SliceStable(decisions, func(i, j int) bool { return decisions[i].Name < decisions[j].Name })
	return decisions, nil
}

// canonicalZones returns the canonical, de-duplicated, sorted zones of a
// route. It never touches the caller's slice.
func canonicalZones(zones []string) []string {
	out := make([]string, 0, len(zones))
	seen := make(map[string]bool, len(zones))
	for _, z := range zones {
		c := dnsname.Canonical(z)
		if !seen[c] {
			seen[c] = true
			out = append(out, c)
		}
	}
	sort.Strings(out)
	return out
}
