// Package conflict decides which ZoneRoutes are accepted when several claim
// the same zone, and rejects routes that claim the cluster domain.
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

// Route is the minimal input for one ZoneRoute.
type Route struct {
	Name    string
	Created time.Time // metadata.creationTimestamp; second resolution from the API
	Zones   []string  // any case, with or without a trailing dot
}

// ZoneConflict records that a zone is already owned by an accepted route.
type ZoneConflict struct {
	Zone   string // canonical form
	Winner string // name of the accepted route that owns Zone
}

// Decision is the outcome for one route. Exactly one state holds:
// Accepted is true, or Reserved is non-empty, or Conflicts is non-empty.
type Decision struct {
	Name      string
	Accepted  bool
	Reserved  []string       // canonical zones inside the cluster domain, sorted
	Conflicts []ZoneConflict // sorted by Zone
}

// Resolve decides acceptance for every route.
//
// Routes claiming the cluster domain or any of its descendants are rejected
// outright and own nothing. The remaining routes are processed in
// (Created, Name) order: a route whose zones are all unowned is accepted
// and takes ownership of them; a route with any zone already owned by an
// earlier accepted route is rejected and owns nothing. Ownership therefore
// only ever points to an accepted route, and the oldest claim wins, with
// the name as a total tie-break for equal timestamps.
//
// clusterDomain is a required safety input: an empty value is an error, so
// a broken controller configuration can never let the cluster domain be
// claimed. Output is sorted by Name and is identical for any permutation
// of routes. Inputs are not mutated.
func Resolve(routes []Route, clusterDomain string) ([]Decision, error) {
	if strings.TrimSpace(clusterDomain) == "" {
		return nil, errors.New("conflict: cluster domain must not be empty")
	}
	domain := dnsname.Canonical(clusterDomain)
	descendant := "." + domain

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
