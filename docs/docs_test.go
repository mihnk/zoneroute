// Package docs holds the drift checks for the user documentation: links and
// paths resolve, ZoneRoute examples decode strictly into the current API,
// condition and reason names are real, and the integration strings agree
// with the code and the wiring script.
package docs

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	utilyaml "k8s.io/apimachinery/pkg/util/yaml"

	"github.com/mihnk/zoneroute/api/v1alpha1"
	"github.com/mihnk/zoneroute/internal/corefile"
)

var docFiles = []string{"../README.md", "install.md", "coredns-wiring.md"}

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
	nouns := map[string]bool{
		"ZoneRoute": true, "ConfigMap": true, "Corefile": true, "CoreDNS": true, "Kubernetes": true,
		"Deployment": true, "ClusterRole": true, "ClusterRoleBinding": true, "Role": true, "RoleBinding": true,
		"ServiceAccount": true, "Namespace": true, "Lease": true, "CustomResourceDefinition": true,
		"KUBECONFIG": true, "IMAGE": true, "LICENSE": true,
	}
	for _, f := range docFiles {
		for _, m := range backtickRE.FindAllStringSubmatch(read(t, f), -1) {
			word := m[1]
			// `Accepted=False` / `ZoneConflict` style tokens.
			for _, part := range regexp.MustCompile(`[=/]`).Split(word, -1) {
				if !camelRE.MatchString(part) || nouns[part] || part == "True" || part == "False" {
					continue
				}
				if !known[part] {
					t.Errorf("%s: `%s` looks like a condition or reason but is not in api/v1alpha1", f, word)
				}
			}
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

	for _, f := range docFiles {
		text := read(t, f)
		for _, s := range []string{importLine, mountPath, corefile.ManagedKey, "coredns-custom"} {
			if !strings.Contains(text, s) {
				t.Errorf("%s does not mention %q", f, s)
			}
		}
		if strings.Contains(text, "subPath:") {
			t.Errorf("%s shows a subPath mount", f)
		}
	}
}
