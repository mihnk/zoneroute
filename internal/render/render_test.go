package render_test

import (
	"bytes"
	"flag"
	"math/rand/v2"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/mihnk/zoneroute/internal/corefile"
	"github.com/mihnk/zoneroute/internal/render"
)

var update = flag.Bool("update", false, "rewrite golden files")

func golden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", name+".golden")
	if *update {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("output differs from %s\n--- got ---\n%s--- want ---\n%s", path, got, want)
	}
}

func mustRender(t *testing.T, routes []render.Route) []byte {
	t.Helper()
	out, err := render.Render(routes)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	return out
}

var (
	corporate = render.Route{
		Name:       "corporate",
		Generation: 3,
		Zones:      []string{"company.local", "corp.internal"},
		Upstreams:  []render.Upstream{{Address: "10.10.10.53"}, {Address: "10.10.10.54", Port: 5353}},
	}
	aws = render.Route{
		Name:       "aws",
		Generation: 1,
		Zones:      []string{"aws.internal"},
		Upstreams:  []render.Upstream{{Address: "10.20.0.2", Port: 53}},
	}
)

func TestRenderGolden(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		golden(t, "empty", mustRender(t, nil))
	})
	t.Run("single", func(t *testing.T) {
		golden(t, "single", mustRender(t, []render.Route{{
			Name:      "single",
			Zones:     []string{"example.com"},
			Upstreams: []render.Upstream{{Address: "10.0.0.1"}},
		}}))
	})
	t.Run("realistic", func(t *testing.T) {
		golden(t, "realistic", mustRender(t, []render.Route{corporate, aws}))
	})
}

func TestRenderBlockLines(t *testing.T) {
	tests := []struct {
		name  string
		route render.Route
		want  string // the zone line and the forward line, joined by "\n"
	}{
		{
			name:  "zones are sorted canonically",
			route: render.Route{Name: "r", Zones: []string{"corp.internal", "company.local"}, Upstreams: []render.Upstream{{Address: "10.0.0.1"}}},
			want:  "company.local corp.internal {\n    forward . 10.0.0.1 {",
		},
		{
			name:  "zone case and trailing dot are normalized",
			route: render.Route{Name: "r", Zones: []string{"Example.COM."}, Upstreams: []render.Upstream{{Address: "10.0.0.1"}}},
			want:  "example.com {\n    forward . 10.0.0.1 {",
		},
		{
			name:  "explicit port 53 is omitted",
			route: render.Route{Name: "r", Zones: []string{"a.test"}, Upstreams: []render.Upstream{{Address: "10.0.0.1", Port: 53}}},
			want:  "a.test {\n    forward . 10.0.0.1 {",
		},
		{
			name:  "port 0 is treated as the default and omitted",
			route: render.Route{Name: "r", Zones: []string{"a.test"}, Upstreams: []render.Upstream{{Address: "10.0.0.1", Port: 0}}},
			want:  "a.test {\n    forward . 10.0.0.1 {",
		},
		{
			name:  "non-default port is written",
			route: render.Route{Name: "r", Zones: []string{"a.test"}, Upstreams: []render.Upstream{{Address: "10.0.0.1", Port: 5353}}},
			want:  "a.test {\n    forward . 10.0.0.1:5353 {",
		},
		{
			name:  "IPv6 without port is bare",
			route: render.Route{Name: "r", Zones: []string{"a.test"}, Upstreams: []render.Upstream{{Address: "2001:db8::1"}}},
			want:  "a.test {\n    forward . 2001:db8::1 {",
		},
		{
			name:  "IPv6 with port is bracketed",
			route: render.Route{Name: "r", Zones: []string{"a.test"}, Upstreams: []render.Upstream{{Address: "2001:db8::1", Port: 5353}}},
			want:  "a.test {\n    forward . [2001:db8::1]:5353 {",
		},
		{
			name: "upstream order is preserved",
			route: render.Route{Name: "r", Zones: []string{"a.test"}, Upstreams: []render.Upstream{
				{Address: "10.0.0.3"}, {Address: "10.0.0.1"}, {Address: "10.0.0.2"},
			}},
			want: "a.test {\n    forward . 10.0.0.3 10.0.0.1 10.0.0.2 {",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out := string(mustRender(t, []render.Route{tc.route}))
			if !strings.Contains(out, tc.want+"\n") {
				t.Errorf("output does not contain %q:\n%s", tc.want, out)
			}
		})
	}
}

func TestRenderGenerationComment(t *testing.T) {
	out := string(mustRender(t, []render.Route{{
		Name: "corporate", Generation: 7, Zones: []string{"a.test"},
		Upstreams: []render.Upstream{{Address: "10.0.0.1"}},
	}}))
	if !strings.Contains(out, "# zoneroute: corporate (generation 7)\n") {
		t.Errorf("generation comment missing:\n%s", out)
	}
}

func TestRenderIsDeterministic(t *testing.T) {
	third := render.Route{Name: "middle", Zones: []string{"m.test"}, Upstreams: []render.Upstream{{Address: "10.0.0.9"}}}
	routes := []render.Route{corporate, aws, third}
	want := mustRender(t, routes)

	t.Run("reversed input", func(t *testing.T) {
		got := mustRender(t, []render.Route{third, aws, corporate})
		if !bytes.Equal(got, want) {
			t.Errorf("reversed input produced different bytes")
		}
	})

	t.Run("shuffled input", func(t *testing.T) {
		for i := range 50 {
			shuffled := append([]render.Route(nil), routes...)
			rand.Shuffle(len(shuffled), func(a, b int) { shuffled[a], shuffled[b] = shuffled[b], shuffled[a] })
			if got := mustRender(t, shuffled); !bytes.Equal(got, want) {
				t.Fatalf("iteration %d produced different bytes", i)
			}
		}
	})

	t.Run("input slice is not mutated", func(t *testing.T) {
		in := []render.Route{third, aws, corporate}
		mustRender(t, in)
		if in[0].Name != "middle" || in[2].Name != "corporate" {
			t.Errorf("Render reordered the caller's slice")
		}
	})
}

func TestRenderRoundTripsThroughInspect(t *testing.T) {
	out := mustRender(t, []render.Route{corporate, aws})

	res, err := corefile.Inspect(string(out), nil)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	want := map[corefile.Listener]string{
		{Transport: "dns", Zone: "company.local.", Port: 53}: corefile.CorefileSource,
		{Transport: "dns", Zone: "corp.internal.", Port: 53}: corefile.CorefileSource,
		{Transport: "dns", Zone: "aws.internal.", Port: 53}:  corefile.CorefileSource,
	}
	if !reflect.DeepEqual(res.Listeners, want) {
		t.Errorf("Listeners = %v, want %v", res.Listeners, want)
	}
	if res.Wired || len(res.UnresolvedImports) != 0 {
		t.Errorf("fragment must not contain imports: wired=%v unresolved=%v", res.Wired, res.UnresolvedImports)
	}
}

func TestRenderOutputShape(t *testing.T) {
	out := mustRender(t, []render.Route{corporate, aws})
	if !bytes.HasPrefix(out, []byte("# Managed by ZoneRoute. Edits are overwritten.\n")) {
		t.Errorf("header missing")
	}
	if !bytes.HasSuffix(out, []byte("}\n")) || bytes.HasSuffix(out, []byte("\n\n")) {
		t.Errorf("output must end with exactly one newline: %q", out[len(out)-4:])
	}
	if bytes.Contains(out, []byte("\r")) {
		t.Errorf("output contains CR")
	}
	for _, forbidden := range []string{"cache", "errors", "log", "health"} {
		if strings.Contains(string(out), "\n    "+forbidden) {
			t.Errorf("output contains forbidden directive %q", forbidden)
		}
	}
}
