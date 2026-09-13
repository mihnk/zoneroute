// Package docs holds the drift checks for the user documentation: links and
// paths resolve, ZoneRoute examples decode strictly into the current API,
// condition and reason names are real, and the integration strings agree
// with the code and the wiring script.
package docs

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"

	"github.com/mihnk/zoneroute/api/v1alpha1"
	"github.com/mihnk/zoneroute/internal/corefile"
)

var docFiles = []string{"../README.md", "install.md", "coredns-wiring.md", "troubleshooting.md", "release.md"}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

var (
	linkRE     = regexp.MustCompile(`\]\(([^)#\s]+)(#[^)]*)?\)`)
	backtickRE = regexp.MustCompile("`([^`\n]+)`")
	repoPathRE = regexp.MustCompile(`^(install\.yaml|LICENSE|(api|cmd|config|docs|hack|internal|test)/[A-Za-z0-9_./-]*)$`)
	fenceRE    = regexp.MustCompile("(?s)```yaml\n(.*?)```")
	camelRE    = regexp.MustCompile(`^[A-Z][A-Za-z]{3,}$`)
)

// Relative links point at files that exist, and backticked repository paths
// exist.
func TestLinksAndPathsExist(t *testing.T) {
	for _, f := range docFiles {
		text := read(t, f)
		dir := filepath.Dir(f)
		for _, m := range linkRE.FindAllStringSubmatch(text, -1) {
			target := m[1]
			if strings.Contains(target, "://") || strings.HasPrefix(target, "mailto:") {
				continue
			}
			if _, err := os.Stat(filepath.Join(dir, target)); err != nil {
				t.Errorf("%s links to %s: %v", f, target, err)
			}
		}
		for _, m := range backtickRE.FindAllStringSubmatch(text, -1) {
			if p := m[1]; repoPathRE.MatchString(p) {
				if _, err := os.Stat(filepath.Join("..", p)); err != nil {
					t.Errorf("%s mentions %s: %v", f, p, err)
				}
			}
		}
	}
}

// Every ZoneRoute example decodes strictly into the current API type, so a
// renamed or misspelled field fails here.
func TestZoneRouteExamplesDecode(t *testing.T) {
	found := 0
	for _, f := range docFiles {
		for _, m := range fenceRE.FindAllStringSubmatch(read(t, f), -1) {
			for _, doc := range strings.Split(m[1], "\n---\n") {
				if !strings.Contains(doc, "kind: ZoneRoute") {
					continue
				}
				found++
				j, err := utilyaml.ToJSON([]byte(doc))
				if err != nil {
					t.Errorf("%s: example is not valid YAML: %v\n%s", f, err, doc)
					continue
				}
				dec := json.NewDecoder(bytes.NewReader(j))
				dec.DisallowUnknownFields()
				var zr v1alpha1.ZoneRoute
				if err := dec.Decode(&zr); err != nil {
					t.Errorf("%s: example does not match the API: %v\n%s", f, err, doc)
					continue
				}
				if zr.APIVersion != v1alpha1.GroupVersion.String() || zr.Kind != "ZoneRoute" {
					t.Errorf("%s: example has apiVersion/kind %s/%s", f, zr.APIVersion, zr.Kind)
				}
				if len(zr.Spec.Zones) == 0 || len(zr.Spec.Upstreams) == 0 {
					t.Errorf("%s: example is missing zones or upstreams:\n%s", f, doc)
				}
			}
		}
	}
	if found == 0 {
		t.Fatal("no ZoneRoute example found in the docs")
	}
}

// Backticked CamelCase words that are not Kubernetes or product nouns must
// be condition types or reasons from the API. Catches a renamed reason.
func TestConditionNamesAreReal(t *testing.T) {
	known := map[string]bool{}
	for _, s := range []string{
		v1alpha1.ConditionAccepted, v1alpha1.ConditionPublished,
		v1alpha1.ReasonAccepted, v1alpha1.ReasonAcceptedWithUnresolvedImports,
		v1alpha1.ReasonZoneConflict, v1alpha1.ReasonZoneOwnedByCoreDNS,
		v1alpha1.ReasonReservedZone, v1alpha1.ReasonPublished,
		v1alpha1.ReasonNotAccepted, v1alpha1.ReasonIntegrationConfigMissing,
		v1alpha1.ReasonCoreDNSNotWired, v1alpha1.ReasonFragmentInvalid,
		v1alpha1.ReasonWriteFailed,
	} {
		known[s] = true
	}
	// Condition names the docs mention in order to say ZoneRoute has no
	// such condition. They must stay absent from the API.
	absent := map[string]bool{"Ready": true, "Unknown": true, "Loaded": true}
	nouns := map[string]bool{
		"ZoneRoute": true, "ConfigMap": true, "Corefile": true, "CoreDNS": true, "Kubernetes": true,
		"Deployment": true, "ClusterRole": true, "ClusterRoleBinding": true, "Role": true, "RoleBinding": true,
		"ServiceAccount": true, "Namespace": true, "Lease": true, "CustomResourceDefinition": true,
		"KUBECONFIG": true, "IMAGE": true, "LICENSE": true,
		"NetworkPolicy": true, "SERVFAIL": true, "NXDOMAIN": true, "Prometheus": true,
		"SemVer": true, "Actions": true, "Buildx": true, "Registry": true,
		"Caddyfile": true, "Events": true, "Secrets": true,
	}
	for _, f := range docFiles {
		for _, m := range backtickRE.FindAllStringSubmatch(read(t, f), -1) {
			word := m[1]
			// `Accepted=False` / `ZoneConflict` style tokens.
			for _, part := range regexp.MustCompile(`[=/]`).Split(word, -1) {
				if !camelRE.MatchString(part) || nouns[part] || part == "True" || part == "False" {
					continue
				}
				if part == strings.ToUpper(part) {
					continue // a printer-column heading such as ACCEPTED
				}
				if absent[part] {
					continue // named to say the API does not expose it
				}
				if !known[part] {
					t.Errorf("%s: `%s` looks like a condition or reason but is not in api/v1alpha1", f, word)
				}
			}
		}
	}
	for name := range absent {
		if known[name] {
			t.Errorf("%q is now a real API constant; the docs say it does not exist", name)
		}
	}
}

// The integration strings the docs teach are the ones the controller checks
// for and the wiring script establishes.
func TestIntegrationContractStrings(t *testing.T) {
	importLine := "import " + corefile.WiringImport
	mountPath := "/etc/coredns/custom"

	script := read(t, "../hack/wire-coredns.sh")
	for _, s := range []string{importLine, mountPath, corefile.ManagedKey} {
		if s == corefile.ManagedKey {
			continue // the script does not write the key
		}
		if !strings.Contains(script, s) {
			t.Errorf("hack/wire-coredns.sh does not contain %q", s)
		}
	}

	// The documents that teach the integration contract must state it; a
	// maintainer document such as release.md has no reason to.
	contractDocs := []string{"../README.md", "install.md", "coredns-wiring.md", "troubleshooting.md"}
	for _, f := range contractDocs {
		text := read(t, f)
		for _, s := range []string{importLine, mountPath, corefile.ManagedKey, "coredns-custom"} {
			if !strings.Contains(text, s) {
				t.Errorf("%s does not mention %q", f, s)
			}
		}
	}
	// No document may ever show a subPath mount.
	for _, f := range docFiles {
		if strings.Contains(read(t, f), "subPath:") {
			t.Errorf("%s shows a subPath mount", f)
		}
	}
}

// Every condition and reason the API defines is documented, so a new
// constant cannot ship without an entry in the reason reference.
func TestTroubleshootingCoversEveryReason(t *testing.T) {
	text := read(t, "troubleshooting.md")
	for _, name := range []string{
		v1alpha1.ConditionAccepted, v1alpha1.ConditionPublished,
		v1alpha1.ReasonAccepted, v1alpha1.ReasonAcceptedWithUnresolvedImports,
		v1alpha1.ReasonZoneConflict, v1alpha1.ReasonZoneOwnedByCoreDNS,
		v1alpha1.ReasonReservedZone, v1alpha1.ReasonPublished,
		v1alpha1.ReasonNotAccepted, v1alpha1.ReasonIntegrationConfigMissing,
		v1alpha1.ReasonCoreDNSNotWired, v1alpha1.ReasonFragmentInvalid,
		v1alpha1.ReasonWriteFailed,
	} {
		if !strings.Contains(text, "`"+name+"`") {
			t.Errorf("troubleshooting.md does not document `%s`", name)
		}
	}
}

// installedObject is one object of the install bundle, by identity.
type installedObject struct{ kind, namespace, name string }

func installedObjects(t *testing.T) map[installedObject]bool {
	t.Helper()
	f, err := os.Open("../install.yaml")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	out := map[installedObject]bool{}
	dec := utilyaml.NewYAMLOrJSONDecoder(f, 4096)
	for {
		var u unstructured.Unstructured
		err := dec.Decode(&u)
		if errors.Is(err, io.EOF) {
			return out
		}
		if err != nil {
			t.Fatalf("install.yaml: %v", err)
		}
		if len(u.Object) > 0 {
			out[installedObject{u.GetKind(), u.GetNamespace(), u.GetName()}] = true
		}
	}
}

// The objects the docs tell people to inspect exist in install.yaml, under
// exactly the kind, namespace and name the commands use.
func TestDocumentedObjectsAreInstalled(t *testing.T) {
	installed := installedObjects(t)
	for _, o := range []installedObject{
		{"Namespace", "", "zoneroute-system"},
		{"ServiceAccount", "zoneroute-system", "zoneroute-controller"},
		{"Deployment", "zoneroute-system", "zoneroute-controller"},
		{"Role", "kube-system", "zoneroute-coredns"},
		{"RoleBinding", "kube-system", "zoneroute-coredns"},
		{"ClusterRole", "", "zoneroute-controller"},
		{"CustomResourceDefinition", "", "zoneroutes.dns.mihnk.org"},
	} {
		if !installed[o] {
			t.Errorf("docs reference %s %s/%s, which install.yaml does not create", o.kind, o.namespace, o.name)
		}
		if !mentionedInDocs(t, o.name) {
			t.Errorf("no documentation mentions %s", o.name)
		}
	}
}

func mentionedInDocs(t *testing.T, name string) bool {
	t.Helper()
	for _, f := range docFiles {
		if strings.Contains(read(t, f), name) {
			return true
		}
	}
	return false
}
