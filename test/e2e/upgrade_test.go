//go:build e2e_upgrade

package e2e

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/mihnk/zoneroute/api/v1alpha1"
)

// The upgrade lane proves that a cluster running the released v0.1.0 can be
// upgraded to the working tree in place: apply the new version over the old
// one, and the routes people already created keep working.
//
// hack/upgrade-e2e.sh has already installed the exact v0.1.0 release asset,
// checksum-verified, on a cluster wired the same way the functional suite
// wires it. This file drives the rest: prove v0.1.0 is healthy, upgrade,
// prove nothing was lost.
//
// What it does not do: assert how many times the controller writes the
// ConfigMap. The upgrade contract is that the fragment is right and DNS
// works, not that a reconcile writes a particular number of times.

const (
	// The image the v0.1.0 release manifest pins, by digest. hack/upgrade-e2e.sh
	// pins the manifest's checksum; this pins what that manifest runs.
	releasedImage = "ghcr.io/mihnk/zoneroute@sha256:01d5a9c73ff82111ee3b70b419cf5600bf54004de05718afcafddc0775e7b6c3"

	// The image hack/e2e.sh builds from the working tree and loads into kind.
	workingTreeImage = "zoneroute-controller:e2e"

	upgradeRoute = "upgrade"

	// The same zone and name the functional suite uses. They are declared
	// here rather than shared because scenarios_test.go is built under the
	// e2e tag and this file under e2e_upgrade; the two never compile
	// together, so there is no conflict to resolve.
	upgradeZone  = "zoneroute.test"
	upgradeQuery = "hello.zoneroute.test."
)

// identity is what proves an object survived rather than being recreated.
// UIDs are assigned once, at creation; resourceVersion is deliberately
// volatile and is not part of the model.
type identity struct {
	uid        types.UID
	name       string
	generation int64
	zones      []string
	upstreams  []v1alpha1.Upstream
}

func routeIdentity(t *testing.T, name string) identity {
	t.Helper()
	zr := getRoute(t, name)
	return identity{
		uid:        zr.UID,
		name:       zr.Name,
		generation: zr.Generation,
		zones:      append([]string(nil), zr.Spec.Zones...),
		upstreams:  append([]v1alpha1.Upstream(nil), zr.Spec.Upstreams...),
	}
}

func crdUID(t *testing.T) types.UID {
	t.Helper()
	var crd apiextensionsv1.CustomResourceDefinition
	if err := kube.Get(ctx, types.NamespacedName{Name: "zoneroutes.dns.mihnk.org"}, &crd); err != nil {
		t.Fatalf("reading the ZoneRoute CRD: %v", err)
	}
	return crd.UID
}

// controllerImage reports the image the Deployment asks for.
func controllerImage(t *testing.T) string {
	t.Helper()
	var d appsv1.Deployment
	if err := kube.Get(ctx, types.NamespacedName{Namespace: controllerNamespace, Name: "zoneroute-controller"}, &d); err != nil {
		t.Fatalf("reading the controller Deployment: %v", err)
	}
	for _, c := range d.Spec.Template.Spec.Containers {
		if c.Name == "controller" {
			return c.Image
		}
	}
	t.Fatal("the controller Deployment has no container named controller")
	return ""
}

// readyControllerPod returns the single Ready controller pod, and fails if
// there is not exactly one.
func readyControllerPod(t *testing.T) corev1.Pod {
	t.Helper()
	var ready []corev1.Pod
	var seen []string
	for _, p := range controllerPods(t) {
		seen = append(seen, p.Name+"("+string(p.Status.Phase)+")")
		if p.DeletionTimestamp == nil && podReady(p) {
			ready = append(ready, p)
		}
	}
	if len(ready) != 1 {
		t.Fatalf("want exactly one Ready controller pod, got %d: %s", len(ready), strings.Join(seen, ", "))
	}
	return ready[0]
}

// waitControllerOn waits until exactly one Ready controller pod is running
// the wanted image and none of the old pods remain.
func waitControllerOn(t *testing.T, image string, gone map[types.UID]bool) corev1.Pod {
	t.Helper()
	var got corev1.Pod
	eventually(t, rolloutTimeout, "controller running "+image, func() (bool, string) {
		ready, stale := 0, 0
		var observed []string
		var candidate corev1.Pod
		for _, p := range controllerPods(t) {
			img := ""
			for _, c := range p.Spec.Containers {
				if c.Name == "controller" {
					img = c.Image
				}
			}
			observed = append(observed, p.Name+"["+string(p.Status.Phase)+" "+img+"]")
			if gone[p.UID] && p.DeletionTimestamp == nil {
				stale++
				continue
			}
			if p.DeletionTimestamp == nil && podReady(p) && img == image {
				ready++
				candidate = p
			}
		}
		if ready == 1 && stale == 0 {
			got = candidate
			return true, strings.Join(observed, ", ")
		}
		return false, strings.Join(observed, ", ")
	})
	return got
}

// applyWorkingTree performs the upgrade itself: it renders the working
// tree's installation manifests and applies them over the running release.
// Nothing is deleted — this is the `apply new over old` path a user follows.
//
// The command comes from hack/upgrade-e2e.sh so the test and the lane cannot
// disagree about which manifests are "current".
func applyWorkingTree(t *testing.T) {
	t.Helper()
	renderCmd := os.Getenv("ZONEROUTE_UPGRADE_APPLY")
	if renderCmd == "" {
		t.Fatal("ZONEROUTE_UPGRADE_APPLY is not set; run this through hack/upgrade-e2e.sh")
	}

	// `go test` runs in the package directory, and the render command is
	// written relative to the repository root, where hack/upgrade-e2e.sh
	// runs. Point it back at the root, and keep stderr: a failure here used
	// to report only an exit status.
	render := exec.Command("sh", "-c", renderCmd)
	render.Dir = repoRoot(t)
	var stderr bytes.Buffer
	render.Stderr = &stderr
	manifests, err := render.Output()
	if err != nil {
		t.Fatalf("rendering the working-tree manifests (%s) in %s: %v\n%s",
			renderCmd, render.Dir, err, stderr.String())
	}

	// The manifests arrive on stdin, so this one needs no working directory.
	apply := exec.Command("kubectl", "apply", "-f", "-")
	apply.Stdin = bytes.NewReader(manifests)
	out, err := apply.CombinedOutput()
	if err != nil {
		t.Fatalf("applying the working-tree manifests: %v\n%s", err, out)
	}

	// Worth reading in the log: an upgrade should reconfigure, never replace.
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		t.Logf("apply: %s", line)
		if strings.Contains(line, "deleted") {
			t.Errorf("the upgrade deleted a resource: %s", line)
		}
	}
}

// repoRoot is the repository root, two levels above this package.
func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("resolving the working directory: %v", err)
	}
	return filepath.Join(wd, "..", "..")
}

func TestUpgradeFromV010(t *testing.T) {
	// --- the released version, before anything is upgraded ----------------

	if got := controllerImage(t); got != releasedImage {
		t.Fatalf("the cluster is not running the v0.1.0 release: controller image is %q, want %q", got, releasedImage)
	}
	oldPod := readyControllerPod(t)
	t.Logf("v0.1.0 controller: pod %s, image %s", oldPod.Name, releasedImage)

	crdBefore := crdUID(t)

	zr := createRoute(t, upgradeRoute, []string{upgradeZone}, upstream{upstreamA, 53})
	waitCondition(t, upgradeRoute, v1alpha1.ConditionAccepted, metav1.ConditionTrue, v1alpha1.ReasonAccepted)
	waitCondition(t, upgradeRoute, v1alpha1.ConditionPublished, metav1.ConditionTrue, v1alpha1.ReasonPublished)

	wantFragment := expectFragment(t, zr)
	waitFragment(t, wantFragment)

	// A real working baseline: the same transports the functional suite uses.
	waitDNSOver(t, upgradeQuery, answerA, false)
	waitDNSOver(t, upgradeQuery, answerA, true)

	before := routeIdentity(t, upgradeRoute)
	t.Logf("before upgrade: ZoneRoute uid=%s generation=%d, CRD uid=%s", before.uid, before.generation, crdBefore)

	// --- upgrade in place -------------------------------------------------

	// The working tree's own installation path, applied over the release.
	// Nothing is deleted: not the CRD, not the ZoneRoute, not coredns-custom.
	applyWorkingTree(t)

	newPod := waitControllerOn(t, workingTreeImage, map[types.UID]bool{oldPod.UID: true})
	t.Logf("after upgrade: pod %s, image %s (old pod %s is gone)", newPod.Name, workingTreeImage, oldPod.Name)

	if got := controllerImage(t); got != workingTreeImage {
		t.Errorf("Deployment image = %q, want the working-tree image %q", got, workingTreeImage)
	}
	if newPod.UID == oldPod.UID {
		t.Error("the controller pod was not replaced")
	}

	// --- the resources people already had ---------------------------------

	if got := crdUID(t); got != crdBefore {
		t.Errorf("the CRD was recreated: uid %s -> %s", crdBefore, got)
	}

	after := routeIdentity(t, upgradeRoute)
	if after.uid != before.uid {
		t.Errorf("the ZoneRoute was recreated: uid %s -> %s", before.uid, after.uid)
	}
	if after.name != before.name {
		t.Errorf("name changed: %q -> %q", before.name, after.name)
	}
	if after.generation != before.generation {
		t.Errorf("generation changed without a spec edit: %d -> %d", before.generation, after.generation)
	}
	if !equalStrings(before.zones, after.zones) {
		t.Errorf("spec.zones changed: %v -> %v", before.zones, after.zones)
	}
	if !equalUpstreams(before.upstreams, after.upstreams) {
		t.Errorf("spec.upstreams changed: %+v -> %+v", before.upstreams, after.upstreams)
	}

	// --- and the state the new controller produces ------------------------

	waitCondition(t, upgradeRoute, v1alpha1.ConditionAccepted, metav1.ConditionTrue, v1alpha1.ReasonAccepted)
	waitCondition(t, upgradeRoute, v1alpha1.ConditionPublished, metav1.ConditionTrue, v1alpha1.ReasonPublished)

	// waitCondition already requires observedGeneration to match the object's
	// generation, on the condition and on the status; state it once more
	// against the generation captured before the upgrade.
	if got := getRoute(t, upgradeRoute); got.Status.ObservedGeneration != before.generation {
		t.Errorf("status.observedGeneration = %d, want %d", got.Status.ObservedGeneration, before.generation)
	}

	// Byte equality is the right assertion while the renderer is unchanged:
	// an upgrade that reformats the fragment for no reason is a regression.
	// It is an assertion about this upgrade path, not a promise about the
	// fragment's format.
	waitFragment(t, wantFragment)

	// The same route, never recreated, still resolves.
	waitDNSOver(t, upgradeQuery, answerA, false)
	waitDNSOver(t, upgradeQuery, answerA, true)
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func equalUpstreams(a, b []v1alpha1.Upstream) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
