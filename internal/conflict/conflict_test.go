package conflict_test

import (
	"math/rand/v2"
	"reflect"
	"testing"
	"time"

	"github.com/mihnk/zoneroute/internal/conflict"
	"github.com/mihnk/zoneroute/internal/corefile"
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

func owned(name string, os ...conflict.OwnedByCoreDNS) conflict.Decision {
	return conflict.Decision{Name: name, Owned: os}
}

func dns(zone string, port int, source string) conflict.Existing {
	return conflict.Existing{Transport: "dns", Zone: zone, Port: port, Source: source}
}

func resolve(t *testing.T, existing []conflict.Existing, routes ...conflict.Route) []conflict.Decision {
	t.Helper()
	got, err := conflict.Resolve(routes, existing, domain)
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
			got := resolve(t, nil, tc.routes...)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Resolve =\n  %+v\nwant\n  %+v", got, tc.want)
			}
		})
	}
}

func TestResolveExisting(t *testing.T) {
	tests := []struct {
		name     string
		existing []conflict.Existing
		routes   []conflict.Route
		want     []conflict.Decision
	}{
		{
			name:     "zone served by the Corefile on dns/53 is owned",
			existing: []conflict.Existing{dns("example.com.", 53, "Corefile")},
			routes:   []conflict.Route{route("r", t1, "example.com")},
			want:     []conflict.Decision{owned("r", conflict.OwnedByCoreDNS{Zone: "example.com.", Port: 53, Source: "Corefile"})},
		},
		{
			name:     "same zone on a different port is not a conflict",
			existing: []conflict.Existing{dns("example.com.", 1053, "Corefile")},
			routes:   []conflict.Route{route("r", t1, "example.com")},
			want:     []conflict.Decision{accepted("r")},
		},
		{
			name:     "tls listener on its default port is not a conflict",
			existing: []conflict.Existing{{Transport: "tls", Zone: "example.com.", Port: 853, Source: "Corefile"}},
			routes:   []conflict.Route{route("r", t1, "example.com")},
			want:     []conflict.Decision{accepted("r")},
		},
		{
			name:     "dns listener on 853 is not a conflict",
			existing: []conflict.Existing{dns("example.com.", 853, "Corefile")},
			routes:   []conflict.Route{route("r", t1, "example.com")},
			want:     []conflict.Decision{accepted("r")},
		},
		{
			name:     "same zone and port on a different transport is not a conflict",
			existing: []conflict.Existing{{Transport: "tls", Zone: "example.com.", Port: 53, Source: "Corefile"}},
			routes:   []conflict.Route{route("r", t1, "example.com")},
			want:     []conflict.Decision{accepted("r")},
		},
		{
			name:     "existing zone is canonicalized before comparison",
			existing: []conflict.Existing{dns("Example.COM", 53, "Corefile")},
			routes:   []conflict.Route{route("r", t1, "example.com.")},
			want:     []conflict.Decision{owned("r", conflict.OwnedByCoreDNS{Zone: "example.com.", Port: 53, Source: "Corefile"})},
		},
		{
			name:     "child of an existing zone is not a conflict",
			existing: []conflict.Existing{dns("corp.internal.", 53, "Corefile")},
			routes:   []conflict.Route{route("r", t1, "dev.corp.internal")},
			want:     []conflict.Decision{accepted("r")},
		},
		{
			name:     "parent of an existing zone is not a conflict",
			existing: []conflict.Existing{dns("dev.corp.internal.", 53, "Corefile")},
			routes:   []conflict.Route{route("r", t1, "corp.internal")},
			want:     []conflict.Decision{accepted("r")},
		},
		{
			name:     "the root zone never conflicts",
			existing: []conflict.Existing{dns(".", 53, "Corefile")},
			routes:   []conflict.Route{route("r", t1, "anything.test")},
			want:     []conflict.Decision{accepted("r")},
		},
		{
			name:     "source names the coredns-custom key",
			existing: []conflict.Existing{dns("test.internal.", 53, "foo.server")},
			routes:   []conflict.Route{route("r", t1, "test.internal")},
			want:     []conflict.Decision{owned("r", conflict.OwnedByCoreDNS{Zone: "test.internal.", Port: 53, Source: "foo.server"})},
		},
		{
			name:     "owned takes precedence over route conflict",
			existing: []conflict.Existing{dns("a.test.", 53, "Corefile")},
			routes:   []conflict.Route{route("first", t1, "b.test"), route("r", t2, "a.test", "b.test")},
			want:     []conflict.Decision{accepted("first"), owned("r", conflict.OwnedByCoreDNS{Zone: "a.test.", Port: 53, Source: "Corefile"})},
		},
		{
			name:     "an owned route does not own its other zones",
			existing: []conflict.Existing{dns("a.test.", 53, "Corefile")},
			routes:   []conflict.Route{route("R", t1, "a.test", "free.test"), route("S", t2, "free.test")},
			want:     []conflict.Decision{owned("R", conflict.OwnedByCoreDNS{Zone: "a.test.", Port: 53, Source: "Corefile"}), accepted("S")},
		},
		{
			name:     "reserved takes precedence over owned",
			existing: []conflict.Existing{dns("a.test.", 53, "Corefile")},
			routes:   []conflict.Route{route("r", t1, "a.test", "cluster.local")},
			want:     []conflict.Decision{reserved("r", "cluster.local.")},
		},
		{
			name:     "multiple owned zones are sorted by zone",
			existing: []conflict.Existing{dns("b.test.", 53, "Corefile"), dns("a.test.", 53, "foo.server")},
			routes:   []conflict.Route{route("r", t1, "b.test", "a.test")},
			want: []conflict.Decision{owned("r",
				conflict.OwnedByCoreDNS{Zone: "a.test.", Port: 53, Source: "foo.server"},
				conflict.OwnedByCoreDNS{Zone: "b.test.", Port: 53, Source: "Corefile"},
			)},
		},
		{
			name: "duplicate listener: source chosen by bytewise order regardless of input order",
			existing: []conflict.Existing{
				dns("dup.test.", 53, "zzz.server"),
				dns("dup.test.", 53, "aaa.server"),
				dns("dup.test.", 53, "Corefile"),
			},
			routes: []conflict.Route{route("r", t1, "dup.test")},
			want:   []conflict.Decision{owned("r", conflict.OwnedByCoreDNS{Zone: "dup.test.", Port: 53, Source: "Corefile"})},
		},
		{
			name: "duplicate listener: source order is bytewise, so Aaa.server beats Corefile",
			existing: []conflict.Existing{
				dns("dup.test.", 53, "Corefile"),
				dns("dup.test.", 53, "Aaa.server"),
			},
			routes: []conflict.Route{route("r", t1, "dup.test")},
			want:   []conflict.Decision{owned("r", conflict.OwnedByCoreDNS{Zone: "dup.test.", Port: 53, Source: "Aaa.server"})},
		},
		{
			name:     "empty transport is not treated as dns",
			existing: []conflict.Existing{{Transport: "", Zone: "example.com.", Port: 53, Source: "Corefile"}},
			routes:   []conflict.Route{route("r", t1, "example.com")},
			want:     []conflict.Decision{accepted("r")},
		},
		{
			name:     "no existing listeners behaves like the route-only resolver",
			existing: []conflict.Existing{},
			routes:   []conflict.Route{route("a", t1, "corp.test"), route("b", t2, "corp.test")},
			want:     []conflict.Decision{accepted("a"), conflicted("b", conflict.ZoneConflict{Zone: "corp.test.", Winner: "a"})},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := resolve(t, tc.existing, tc.routes...)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Resolve =\n  %+v\nwant\n  %+v", got, tc.want)
			}
		})
	}
}

func TestResolveClusterDomain(t *testing.T) {
	t.Run("empty cluster domain is an error", func(t *testing.T) {
		for _, cd := range []string{"", "   "} {
			if _, err := conflict.Resolve([]conflict.Route{route("r", t1, "a.test")}, nil, cd); err == nil {
				t.Errorf("Resolve(%q): want error, got nil", cd)
			}
		}
	})

	t.Run("cluster domain is canonicalized", func(t *testing.T) {
		got, err := conflict.Resolve([]conflict.Route{route("r", t1, "svc.cluster.local")}, nil, "Cluster.Local.")
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
	existing := []conflict.Existing{
		dns(".", 53, "Corefile"),
		dns("owned.test.", 53, "zzz.server"),
		dns("owned.test.", 53, "aaa.server"),
		dns("x.test.", 1053, "Corefile"),
		{Transport: "tls", Zone: "y.test.", Port: 853, Source: "Corefile"},
	}
	routes := []conflict.Route{
		route("A", t1, "x.test", "y.test"),
		route("B", t2, "y.test", "z.test"),
		route("C", t3, "z.test"),
		route("R", t1, "ok.test", "cluster.local"),
		route("S", t2, "ok.test"),
		route("O", t1, "owned.test", "free.test"),
		route("F", t2, "free.test"),
		route("zeta", t2, "tie.test"),
		route("alpha", t2, "tie.test"),
	}
	want := resolve(t, existing, routes...)

	for i := range 50 {
		rs := append([]conflict.Route(nil), routes...)
		es := append([]conflict.Existing(nil), existing...)
		rand.Shuffle(len(rs), func(a, b int) { rs[a], rs[b] = rs[b], rs[a] })
		rand.Shuffle(len(es), func(a, b int) { es[a], es[b] = es[b], es[a] })
		if got := resolve(t, es, rs...); !reflect.DeepEqual(got, want) {
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
	existing := []conflict.Existing{
		dns("Owned.Test.", 53, "zzz.server"),
		dns("owned.test", 53, "aaa.server"),
	}
	routesSnapshot := []conflict.Route{
		{Name: "b", Created: t2, Zones: []string{"y.test", "cluster.local"}},
		{Name: "a", Created: t1, Zones: []string{"Y.Test", "x.test."}},
	}
	existingSnapshot := []conflict.Existing{
		dns("Owned.Test.", 53, "zzz.server"),
		dns("owned.test", 53, "aaa.server"),
	}

	resolve(t, existing, routes...)

	if !reflect.DeepEqual(routes, routesSnapshot) {
		t.Errorf("routes mutated:\n got %+v\nwant %+v", routes, routesSnapshot)
	}
	if !reflect.DeepEqual(existing, existingSnapshot) {
		t.Errorf("existing mutated:\n got %+v\nwant %+v", existing, existingSnapshot)
	}
	if zonesA[0] != "Y.Test" || zonesB[1] != "cluster.local" {
		t.Errorf("backing zone slices mutated: %v %v", zonesA, zonesB)
	}
}

// existingFromInspect is the trivial adapter the controller will implement:
// one Existing per visible listener, carrying its source.
func existingFromInspect(res corefile.Result) []conflict.Existing {
	out := make([]conflict.Existing, 0, len(res.Listeners))
	for l, source := range res.Listeners {
		out = append(out, conflict.Existing{Transport: l.Transport, Zone: l.Zone, Port: l.Port, Source: source})
	}
	return out
}

func TestResolveRoundTripsThroughCorefile(t *testing.T) {
	const corefileText = ".:53 {\n    forward . /etc/resolv.conf\n}\nexample.com:1053 {\n    forward . 10.0.0.9\n}\nimport custom/*.server\n"
	servers := map[string]string{
		"foo.server":       "test.internal {\n    forward . 10.0.0.1\n}\n",
		"zoneroute.server": "managed.test {\n    forward . 10.0.0.2\n}\n",
	}

	res, err := corefile.Inspect(corefileText, servers)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}

	got := resolve(t, existingFromInspect(res),
		route("root-child", t1, "anything.test"),   // root "." is a parent, not a conflict
		route("other-port", t1, "example.com"),     // Corefile serves it on 1053, not 53
		route("custom-owned", t1, "test.internal"), // served by foo.server
		route("managed", t1, "managed.test"),       // zoneroute.server is excluded by corefile
	)
	want := []conflict.Decision{
		owned("custom-owned", conflict.OwnedByCoreDNS{Zone: "test.internal.", Port: 53, Source: "foo.server"}),
		accepted("managed"),
		accepted("other-port"),
		accepted("root-child"),
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Resolve =\n  %+v\nwant\n  %+v", got, want)
	}
}
