package corefile

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func dns(zone string, port int) Listener { return Listener{Transport: "dns", Zone: zone, Port: port} }

func TestInspectListeners(t *testing.T) {
	tests := []struct {
		name     string
		corefile string
		want     map[Listener]string
	}{
		{
			name:     "root block on 53",
			corefile: ".:53 {\n  forward . /etc/resolv.conf\n}\n",
			want:     map[Listener]string{dns(".", 53): CorefileSource},
		},
		{
			name:     "zone without port defaults to 53",
			corefile: "company.local {\n  forward . 10.0.0.1\n}\n",
			want:     map[Listener]string{dns("company.local.", 53): CorefileSource},
		},
		{
			name:     "case is normalized",
			corefile: "Example.COM {\n}\n",
			want:     map[Listener]string{dns("example.com.", 53): CorefileSource},
		},
		{
			name:     "trailing dot is normalized",
			corefile: "example.com. {\n}\n",
			want:     map[Listener]string{dns("example.com.", 53): CorefileSource},
		},
		{
			name:     "explicit port 53",
			corefile: "example.com:53 {\n}\n",
			want:     map[Listener]string{dns("example.com.", 53): CorefileSource},
		},
		{
			name:     "non-default port",
			corefile: "example.com:1053 {\n}\n",
			want:     map[Listener]string{dns("example.com.", 1053): CorefileSource},
		},
		{
			name:     "tls transport defaults to 853",
			corefile: "tls://example.com {\n}\n",
			want:     map[Listener]string{{Transport: "tls", Zone: "example.com.", Port: 853}: CorefileSource},
		},
		{
			name:     "grpc transport defaults to 443",
			corefile: "grpc://example.com {\n}\n",
			want:     map[Listener]string{{Transport: "grpc", Zone: "example.com.", Port: 443}: CorefileSource},
		},
		{
			name:     "quic transport defaults to 853",
			corefile: "quic://example.com {\n}\n",
			want:     map[Listener]string{{Transport: "quic", Zone: "example.com.", Port: 853}: CorefileSource},
		},
		{
			name:     "https transport defaults to 443",
			corefile: "https://example.com {\n}\n",
			want:     map[Listener]string{{Transport: "https", Zone: "example.com.", Port: 443}: CorefileSource},
		},
		{
			name:     "https3 transport defaults to 443",
			corefile: "https3://example.com {\n}\n",
			want:     map[Listener]string{{Transport: "https3", Zone: "example.com.", Port: 443}: CorefileSource},
		},
		{
			name:     "explicit dns scheme",
			corefile: "dns://example.com {\n}\n",
			want:     map[Listener]string{dns("example.com.", 53): CorefileSource},
		},
		{
			name:     "same zone and port on different transports are distinct",
			corefile: "dns://example.com:853 {\n}\ntls://example.com {\n}\n",
			want: map[Listener]string{
				dns("example.com.", 853):                            CorefileSource,
				{Transport: "tls", Zone: "example.com.", Port: 853}: CorefileSource,
			},
		},
		{
			name:     "unix socket listener has no port",
			corefile: "unix:///run/coredns.sock {\n}\n",
			want:     map[Listener]string{{Transport: "unix", Zone: "/run/coredns.sock.", Port: 0}: CorefileSource},
		},
		{
			name:     "multiple zones in one block",
			corefile: "a.com b.org:1053 {\n  forward . 10.0.0.1\n}\n",
			want: map[Listener]string{
				dns("a.com.", 53):   CorefileSource,
				dns("b.org.", 1053): CorefileSource,
			},
		},
		{
			name:     "snippet definition is not a listener",
			corefile: "(common) {\n  errors\n}\n.:53 {\n  import common\n}\n",
			want:     map[Listener]string{dns(".", 53): CorefileSource},
		},
		{
			name:     "empty text",
			corefile: "",
			want:     map[Listener]string{},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Inspect(tc.corefile, nil)
			if err != nil {
				t.Fatalf("Inspect: %v", err)
			}
			if !reflect.DeepEqual(got.Listeners, tc.want) {
				t.Errorf("Listeners = %v, want %v", got.Listeners, tc.want)
			}
			if got.Wired {
				t.Errorf("Wired = true, want false")
			}
			if len(got.UnresolvedImports) != 0 {
				t.Errorf("UnresolvedImports = %v, want none", got.UnresolvedImports)
			}
		})
	}
}

func TestInspectImports(t *testing.T) {
	tests := []struct {
		name      string
		corefile  string
		wantWired bool
		wantUnres []Import
		wantZones map[Listener]string
	}{
		{
			name:      "top-level wiring import is detected",
			corefile:  ".:53 {\n  forward . /etc/resolv.conf\n}\nimport custom/*.server\n",
			wantWired: true,
			wantZones: map[Listener]string{dns(".", 53): CorefileSource},
		},
		{
			name:      "quoted wiring import is detected",
			corefile:  "import \"custom/*.server\"\n.:53 {\n}\n",
			wantWired: true,
			wantZones: map[Listener]string{dns(".", 53): CorefileSource},
		},
		{
			name:      "missing wiring import",
			corefile:  ".:53 {\n  forward . /etc/resolv.conf\n}\n",
			wantWired: false,
			wantZones: map[Listener]string{dns(".", 53): CorefileSource},
		},
		{
			name:      "plain-file import is reported and does not break parsing",
			corefile:  ".:53 {\n}\nimport /etc/coredns/extra.conf\n",
			wantUnres: []Import{{Source: CorefileSource, Line: 3, Pattern: "/etc/coredns/extra.conf"}},
			wantZones: map[Listener]string{dns(".", 53): CorefileSource},
		},
		{
			name:      "unrelated glob import is reported",
			corefile:  "import /etc/coredns/extra/*.conf\n.:53 {\n}\n",
			wantUnres: []Import{{Source: CorefileSource, Line: 1, Pattern: "/etc/coredns/extra/*.conf"}},
			wantZones: map[Listener]string{dns(".", 53): CorefileSource},
		},
		{
			name:      "block-level import is ignored",
			corefile:  ".:53 {\n  import custom/*.override\n  import /etc/coredns/plugins.conf\n}\n",
			wantZones: map[Listener]string{dns(".", 53): CorefileSource},
		},
		{
			name:      "wiring import inside a block does not count as wired",
			corefile:  ".:53 {\n  import custom/*.server\n}\n",
			wantWired: false,
			wantZones: map[Listener]string{dns(".", 53): CorefileSource},
		},
		{
			name:      "snippet import is not unresolved",
			corefile:  "(common) {\n  errors\n}\nimport common\n.:53 {\n}\n",
			wantZones: map[Listener]string{dns(".", 53): CorefileSource},
		},
		{
			name:      "commented import is ignored",
			corefile:  "# import /etc/coredns/extra.conf\n.:53 {\n}\n",
			wantZones: map[Listener]string{dns(".", 53): CorefileSource},
		},
		{
			name:     "imports are ordered by line",
			corefile: "import /b.conf\n.:53 {\n}\nimport /a.conf\n",
			wantUnres: []Import{
				{Source: CorefileSource, Line: 1, Pattern: "/b.conf"},
				{Source: CorefileSource, Line: 4, Pattern: "/a.conf"},
			},
			wantZones: map[Listener]string{dns(".", 53): CorefileSource},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Inspect(tc.corefile, nil)
			if err != nil {
				t.Fatalf("Inspect: %v", err)
			}
			if got.Wired != tc.wantWired {
				t.Errorf("Wired = %v, want %v", got.Wired, tc.wantWired)
			}
			if !reflect.DeepEqual(got.UnresolvedImports, tc.wantUnres) {
				t.Errorf("UnresolvedImports = %v, want %v", got.UnresolvedImports, tc.wantUnres)
			}
			if !reflect.DeepEqual(got.Listeners, tc.wantZones) {
				t.Errorf("Listeners = %v, want %v", got.Listeners, tc.wantZones)
			}
		})
	}
}

func TestInspectServers(t *testing.T) {
	corefile := ".:53 {\n  forward . /etc/resolv.conf\n}\nimport custom/*.server\n"

	t.Run("zones from another server key are attributed to it", func(t *testing.T) {
		got, err := Inspect(corefile, map[string]string{
			"foo.server": "test.internal {\n  forward . 10.0.0.1\n}\n",
		})
		if err != nil {
			t.Fatalf("Inspect: %v", err)
		}
		want := map[Listener]string{
			dns(".", 53):              CorefileSource,
			dns("test.internal.", 53): "foo.server",
		}
		if !reflect.DeepEqual(got.Listeners, want) {
			t.Errorf("Listeners = %v, want %v", got.Listeners, want)
		}
	})

	t.Run("managed key is not an existing owner", func(t *testing.T) {
		got, err := Inspect(corefile, map[string]string{
			ManagedKey: "company.local {\n  forward . 10.0.0.1\n}\n",
		})
		if err != nil {
			t.Fatalf("Inspect: %v", err)
		}
		if _, found := got.Listeners[dns("company.local.", 53)]; found {
			t.Errorf("zone from %s must not be reported as an existing owner", ManagedKey)
		}
	})

	t.Run("override keys are not parsed", func(t *testing.T) {
		got, err := Inspect(corefile, map[string]string{
			"log.override": "log\nerrors\n",
		})
		if err != nil {
			t.Fatalf("Inspect: %v", err)
		}
		if len(got.Listeners) != 1 {
			t.Errorf("Listeners = %v, want only the Corefile root", got.Listeners)
		}
	})

	t.Run("first source wins for a duplicate listener", func(t *testing.T) {
		got, err := Inspect(corefile, map[string]string{
			"dup.server": ".:53 {\n}\n",
		})
		if err != nil {
			t.Fatalf("Inspect: %v", err)
		}
		if src := got.Listeners[dns(".", 53)]; src != CorefileSource {
			t.Errorf("source = %q, want %q", src, CorefileSource)
		}
	})

	t.Run("unresolved imports in a server key are attributed to it", func(t *testing.T) {
		got, err := Inspect(corefile, map[string]string{
			"foo.server": "import /etc/coredns/more.conf\n",
		})
		if err != nil {
			t.Fatalf("Inspect: %v", err)
		}
		want := []Import{{Source: "foo.server", Line: 1, Pattern: "/etc/coredns/more.conf"}}
		if !reflect.DeepEqual(got.UnresolvedImports, want) {
			t.Errorf("UnresolvedImports = %v, want %v", got.UnresolvedImports, want)
		}
	})

	t.Run("wiring import in a server key is unresolved, not wiring", func(t *testing.T) {
		got, err := Inspect(".:53 {\n}\n", map[string]string{
			"foo.server": "import custom/*.server\n",
		})
		if err != nil {
			t.Fatalf("Inspect: %v", err)
		}
		if got.Wired {
			t.Errorf("Wired = true; only the Corefile can wire")
		}
		if len(got.UnresolvedImports) != 1 || got.UnresolvedImports[0].Source != "foo.server" {
			t.Errorf("UnresolvedImports = %v, want one from foo.server", got.UnresolvedImports)
		}
	})
}

func TestInspectIsDeterministic(t *testing.T) {
	corefile := ".:53 {\n}\nimport custom/*.server\n"
	servers := map[string]string{
		"c.server":   "c.internal {\n}\nimport /c.conf\n",
		"a.server":   "a.internal {\n}\nimport /a.conf\n",
		"b.server":   "b.internal {\n}\nimport /b.conf\n",
		ManagedKey:   "ignored.internal {\n}\n",
		"x.override": "log\n",
	}

	first, err := Inspect(corefile, servers)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	wantOrder := []string{"a.server", "b.server", "c.server"}
	for i, imp := range first.UnresolvedImports {
		if imp.Source != wantOrder[i] {
			t.Fatalf("UnresolvedImports[%d].Source = %q, want %q", i, imp.Source, wantOrder[i])
		}
	}
	for i := range 50 {
		got, err := Inspect(corefile, servers)
		if err != nil {
			t.Fatalf("Inspect: %v", err)
		}
		if !reflect.DeepEqual(got, first) {
			t.Fatalf("iteration %d differs:\n got %+v\nwant %+v", i, got, first)
		}
	}
}

func TestInspectRealCorefiles(t *testing.T) {
	tests := []struct {
		file      string
		wantWired bool
	}{
		{file: "kubeadm.Corefile", wantWired: false},
		{file: "aks.Corefile", wantWired: true},
	}
	for _, tc := range tests {
		t.Run(tc.file, func(t *testing.T) {
			text, err := os.ReadFile(filepath.Join("testdata", tc.file))
			if err != nil {
				t.Fatal(err)
			}
			got, err := Inspect(string(text), nil)
			if err != nil {
				t.Fatalf("Inspect: %v", err)
			}
			want := map[Listener]string{dns(".", 53): CorefileSource}
			if !reflect.DeepEqual(got.Listeners, want) {
				t.Errorf("Listeners = %v, want %v", got.Listeners, want)
			}
			if got.Wired != tc.wantWired {
				t.Errorf("Wired = %v, want %v", got.Wired, tc.wantWired)
			}
			if len(got.UnresolvedImports) != 0 {
				t.Errorf("UnresolvedImports = %v, want none", got.UnresolvedImports)
			}
		})
	}
}

func TestInspectErrors(t *testing.T) {
	tests := []struct {
		name     string
		corefile string
		servers  map[string]string
	}{
		{name: "unbalanced brace", corefile: ".:53 {\n  forward . 1.1.1.1\n"},
		{name: "unknown transport", corefile: "foo://example.com {\n}\n"},
		{name: "non-numeric port", corefile: "example.com:abc {\n}\n"},
		{name: "port out of range", corefile: "example.com:70000 {\n}\n"},
		{name: "broken server key", corefile: ".:53 {\n}\n", servers: map[string]string{"bad.server": "x {\n"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Inspect(tc.corefile, tc.servers); err == nil {
				t.Errorf("Inspect: want error, got nil")
			}
		})
	}
}
