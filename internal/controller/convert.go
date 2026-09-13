package controller

import (
	"sort"

	"github.com/mihnk/zoneroute/api/v1alpha1"
	"github.com/mihnk/zoneroute/internal/conflict"
	"github.com/mihnk/zoneroute/internal/corefile"
	"github.com/mihnk/zoneroute/internal/render"
)

// toConflictRoutes copies what conflict resolution needs from each object.
func toConflictRoutes(items []v1alpha1.ZoneRoute) []conflict.Route {
	routes := make([]conflict.Route, 0, len(items))
	for i := range items {
		routes = append(routes, conflict.Route{
			Name:    items[i].Name,
			Created: items[i].CreationTimestamp.Time,
			Zones:   append([]string(nil), items[i].Spec.Zones...),
		})
	}
	return routes
}

// toExisting flattens the inspected listeners into a deterministic slice.
// The map's iteration order must not leak into anything user-visible.
func toExisting(res corefile.Result) []conflict.Existing {
	existing := make([]conflict.Existing, 0, len(res.Listeners))
	for l, source := range res.Listeners {
		existing = append(existing, conflict.Existing{
			Transport: l.Transport,
			Zone:      l.Zone,
			Port:      l.Port,
			Source:    source,
		})
	}
	sort.Slice(existing, func(i, j int) bool {
		a, b := existing[i], existing[j]
		if a.Transport != b.Transport {
			return a.Transport < b.Transport
		}
		if a.Zone != b.Zone {
			return a.Zone < b.Zone
		}
		if a.Port != b.Port {
			return a.Port < b.Port
		}
		return a.Source < b.Source
	})
	return existing
}

// toRenderRoutes copies the accepted objects into the renderer's input.
// Upstream order is preserved exactly; it is part of the API contract.
func toRenderRoutes(items []v1alpha1.ZoneRoute, accepted map[string]bool) []render.Route {
	routes := make([]render.Route, 0, len(accepted))
	for i := range items {
		if !accepted[items[i].Name] {
			continue
		}
		upstreams := make([]render.Upstream, 0, len(items[i].Spec.Upstreams))
		for _, u := range items[i].Spec.Upstreams {
			upstreams = append(upstreams, render.Upstream{Address: u.Address, Port: u.Port})
		}
		routes = append(routes, render.Route{
			Name:       items[i].Name,
			Generation: items[i].Generation,
			Zones:      append([]string(nil), items[i].Spec.Zones...),
			Upstreams:  upstreams,
		})
	}
	return routes
}
