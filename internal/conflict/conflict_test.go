package conflict_test

import (
	"math/rand/v2"
	"reflect"
	"testing"
	"time"

	"github.com/mihnk/zoneroute/internal/conflict"
)

const domain = "cluster.local"

var (
	t1 = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	t2 = t1.Add(time.Second)
	t3 = t1.Add(2 * time.Second)
)

func route(name string, created time.Time, zones ...string) conflict.Route {
	return conflict.Route{Name: name, Created: created, Zones: zones}
}

func accepted(name string) conflict.Decision {
	return conflict.Decision{Name: name, Accepted: true}
}

func conflicted(name string, cs ...conflict.ZoneConflict) conflict.Decision {
	return conflict.Decision{Name: name, Conflicts: cs}
}

func reserved(name string, zones ...string) conflict.Decision {
	return conflict.Decision{Name: name, Reserved: zones}
}

func resolve(t *testing.T, routes ...conflict.Route) []conflict.Decision {
	t.Helper()
	got, err := conflict.Resolve(routes, domain)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	return got
}

func TestResolve(t *testing.T) {
	tests := []struct {
		name   string
		routes []conflict.Route
		want   []conflict.Decision
	}{
		{
			name:   "same zone, different names: older wins",
			routes: []conflict.Route{route("a", t1, "corp.test"), route("b", t2, "corp.test")},
			want:   []conflict.Decision{accepted("a"), conflicted("b", conflict.ZoneConflict{Zone: "corp.test.", Winner: "a"})},
		},
		{
			name:   "case-insensitive equivalent zones conflict",
			routes: []conflict.Route{route("a", t1, "Example.COM"), route("b", t2, "example.com")},
			want:   []conflict.Decision{accepted("a"), conflicted("b", conflict.ZoneConflict{Zone: "example.com.", Winner: "a"})},
		},
		{
			name:   "trailing-dot equivalent zones conflict",
			routes: []conflict.Route{route("a", t1, "example.com"), route("b", t2, "example.com.")},
			want:   []conflict.Decision{accepted("a"), conflicted("b", conflict.ZoneConflict{Zone: "example.com.", Winner: "a"})},
		},
		{
			name:   "timestamp beats name",
			routes: []conflict.Route{route("newer", t3, "z.test"), route("older", t2, "z.test")},
			want:   []conflict.Decision{conflicted("newer", conflict.ZoneConflict{Zone: "z.test.", Winner: "older"}), accepted("older")},
		},
		{
			name:   "parent and child zones coexist",
			routes: []conflict.Route{route("parent", t1, "corp.internal"), route("child", t2, "dev.corp.internal")},
			want:   []conflict.Decision{accepted("child"), accepted("parent")},
		},
		{
			name:   "different zones coexist",
			routes: []conflict.Route{route("a", t1, "a.test"), route("b", t2, "b.test"), route("c", t3, "c.test")},
			want:   []conflict.Decision{accepted("a"), accepted("b"), accepted("c")},
		},
		{
			name:   "cluster domain itself is reserved",
			routes: []conflict.Route{route("r", t1, "cluster.local")},
			want:   []conflict.Decision{reserved("r", "cluster.local.")},
		},
		{
			name:   "descendants of the cluster domain are reserved",
			routes: []conflict.Route{route("r", t1, "svc.cluster.local", "a.b.svc.cluster.local")},
			want:   []conflict.Decision{reserved("r", "a.b.svc.cluster.local.", "svc.cluster.local.")},
		},
		{
			name:   "similarly named unrelated domains are accepted",
			routes: []conflict.Route{route("a", t1, "notcluster.local"), route("b", t2, "cluster.localhost")},
			want:   []conflict.Decision{accepted("a"), accepted("b")},
		},
		{
			name:   "one valid and one reserved zone rejects the whole route",
			routes: []conflict.Route{route("r", t1, "ok.test", "svc.cluster.local")},
			want:   []conflict.Decision{reserved("r", "svc.cluster.local.")},
		},
		{
			name:   "conflict on one of several zones rejects the whole route and frees the rest",
			routes: []conflict.Route{route("A", t1, "x.test", "y.test"), route("B", t2, "y.test", "z.test")},
			want:   []conflict.Decision{accepted("A"), conflicted("B", conflict.ZoneConflict{Zone: "y.test.", Winner: "A"})},
		},
		{
			name:   "equal timestamps: name bytewise ascending wins",
			routes: []conflict.Route{route("zeta", t1, "tie.test"), route("alpha", t1, "tie.test")},
			want:   []conflict.Decision{accepted("alpha"), conflicted("zeta", conflict.ZoneConflict{Zone: "tie.test.", Winner: "alpha"})},
		},
		{
			name: "cascade: a rejected route owns nothing, so a later route can take its zone",
			routes: []conflict.Route{
				route("A", t1, "x.test", "y.test"),
				route("B", t2, "y.test", "z.test"),
				route("C", t3, "z.test"),
			},
			want: []conflict.Decision{
				accepted("A"),
				conflicted("B", conflict.ZoneConflict{Zone: "y.test.", Winner: "A"}),
				accepted("C"),
			},
		},
		{
			name:   "reserved takes precedence over conflict",
			routes: []conflict.Route{route("a", t1, "dup.test"), route("b", t2, "dup.test", "cluster.local")},
			want:   []conflict.Decision{accepted("a"), reserved("b", "cluster.local.")},
		},
		{
			name:   "a reserved route does not own its other zones",
			routes: []conflict.Route{route("R", t1, "ok.test", "cluster.local"), route("S", t2, "ok.test")},
			want:   []conflict.Decision{reserved("R", "cluster.local."), accepted("S")},
		},
		{
			name:   "empty input",
			routes: nil,
			want:   []conflict.Decision{},
		},
		{
			name: "multiple conflicts are reported sorted by zone",
			routes: []conflict.Route{
				route("a", t1, "b.test", "a.test"),
				route("c", t2, "b.test", "a.test", "c.test"),
			},
			want: []conflict.Decision{
				accepted("a"),
				conflicted("c",
					conflict.ZoneConflict{Zone: "a.test.", Winner: "a"},
					conflict.ZoneConflict{Zone: "b.test.", Winner: "a"},
				),
			},
		},
		{
			name:   "duplicate zones within a route are collapsed",
			routes: []conflict.Route{route("a", t1, "d.test", "D.TEST."), route("b", t2, "d.test")},
			want:   []conflict.Decision{accepted("a"), conflicted("b", conflict.ZoneConflict{Zone: "d.test.", Winner: "a"})},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := resolve(t, tc.routes...)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Resolve =\n  %+v\nwant\n  %+v", got, tc.want)
			}
		})
	}
}

func TestResolveClusterDomain(t *testing.T) {
	t.Run("empty cluster domain is an error", func(t *testing.T) {
		for _, cd := range []string{"", "   "} {
			if _, err := conflict.Resolve([]conflict.Route{route("r", t1, "a.test")}, cd); err == nil {
				t.Errorf("Resolve(%q): want error, got nil", cd)
			}
		}
	})

	t.Run("cluster domain is canonicalized", func(t *testing.T) {
		got, err := conflict.Resolve([]conflict.Route{route("r", t1, "svc.cluster.local")}, "Cluster.Local.")
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		want := []conflict.Decision{reserved("r", "svc.cluster.local.")}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("Resolve = %+v, want %+v", got, want)
		}
	})
}

func TestResolveIsOrderIndependent(t *testing.T) {
	routes := []conflict.Route{
		route("A", t1, "x.test", "y.test"),
		route("B", t2, "y.test", "z.test"),
		route("C", t3, "z.test"),
		route("R", t1, "ok.test", "cluster.local"),
		route("S", t2, "ok.test"),
		route("zeta", t2, "tie.test"),
		route("alpha", t2, "tie.test"),
	}
	want := resolve(t, routes...)

	for i := range 50 {
		shuffled := append([]conflict.Route(nil), routes...)
		rand.Shuffle(len(shuffled), func(a, b int) { shuffled[a], shuffled[b] = shuffled[b], shuffled[a] })
		if got := resolve(t, shuffled...); !reflect.DeepEqual(got, want) {
			t.Fatalf("iteration %d differs:\n got %+v\nwant %+v", i, got, want)
		}
	}
}

func TestResolveDoesNotMutateInput(t *testing.T) {
	zonesA := []string{"Y.Test", "x.test."}
	zonesB := []string{"y.test", "cluster.local"}
	routes := []conflict.Route{
		{Name: "b", Created: t2, Zones: zonesB},
		{Name: "a", Created: t1, Zones: zonesA},
	}
	snapshot := []conflict.Route{
		{Name: "b", Created: t2, Zones: []string{"y.test", "cluster.local"}},
		{Name: "a", Created: t1, Zones: []string{"Y.Test", "x.test."}},
	}

	resolve(t, routes...)

	if !reflect.DeepEqual(routes, snapshot) {
		t.Errorf("input mutated:\n got %+v\nwant %+v", routes, snapshot)
	}
	if zonesA[0] != "Y.Test" || zonesB[1] != "cluster.local" {
		t.Errorf("backing zone slices mutated: %v %v", zonesA, zonesB)
	}
}
