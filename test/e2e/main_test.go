//go:build e2e

// Package e2e is the functional suite. It expects a cluster prepared by
// hack/e2e.sh: KUBECONFIG set, CoreDNS wired to the integration contract,
// the controller installed from the production manifests, and the fixtures
// in manifests/ running. Without the e2e build tag nothing here compiles, so
// `go test ./...` never touches a cluster.
package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	authorizationv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/mihnk/zoneroute/api/v1alpha1"
	"github.com/mihnk/zoneroute/internal/render"
)

const (
	coreDNSNamespace    = "kube-system"
	controllerNamespace = "zoneroute-system"
	fixtureNamespace    = "zoneroute-e2e"
	fragmentKey         = "zoneroute.server"
	importLine          = "import custom/*.server"

	// Answers baked into manifests/upstream.yaml.
	answerA    = "192.0.2.123" // hello.zoneroute.test from upstream-a:53
	answerB    = "192.0.2.223" // hello.child.zoneroute.test from upstream-b:53
	answerPort = "192.0.2.55"  // hello.port.test from upstream-a:5353

	pollInterval   = time.Second
	objectTimeout  = 60 * time.Second  // one reconcile
	rolloutTimeout = 120 * time.Second // pod replacement
	dnsTimeout     = 180 * time.Second // ConfigMap propagation + CoreDNS reload + cache
	settleWindow   = 10 * time.Second  // negative assertions
)

var (
	ctx  = context.Background()
	kube client.Client

	upstreamA, upstreamB string // Service ClusterIPs, discovered at startup
	kubernetesIP         string
	wiredCorefile        string // the Corefile after wiring; must survive the suite
)

func TestMain(m *testing.M) {
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		fail(err)
	}
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		fail(err)
	}
	c, err := client.New(ctrl.GetConfigOrDie(), client.Options{Scheme: scheme})
	if err != nil {
		fail(err)
	}
	kube = c

	upstreamA = serviceIP(fixtureNamespace, "upstream-a")
	upstreamB = serviceIP(fixtureNamespace, "upstream-b")
	kubernetesIP = serviceIP("default", "kubernetes")

	cm, ok := getConfigMap(coreDNSNamespace, "coredns")
	if !ok {
		fail(fmt.Errorf("kube-system/coredns does not exist"))
	}
	wiredCorefile = cm.Data["Corefile"]
	if !strings.Contains(wiredCorefile, importLine) {
		fail(fmt.Errorf("Corefile is not wired; run hack/wire-coredns.sh"))
	}

	os.Exit(m.Run())
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "e2e:", err)
	os.Exit(1)
}

func serviceIP(namespace, name string) string {
	var svc corev1.Service
	if err := kube.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, &svc); err != nil {
		fail(fmt.Errorf("reading Service %s/%s: %w", namespace, name, err))
	}
	if svc.Spec.ClusterIP == "" || svc.Spec.ClusterIP == corev1.ClusterIPNone {
		fail(fmt.Errorf("Service %s/%s has no ClusterIP", namespace, name))
	}
	return svc.Spec.ClusterIP
}

// eventually polls fn until it reports ok. The failure message carries what
// was last observed, so a timeout explains itself.
func eventually(t *testing.T, timeout time.Duration, what string, fn func() (ok bool, observed string)) {
	t.Helper()
	var last string
	err := wait.PollUntilContextTimeout(ctx, pollInterval, timeout, true, func(context.Context) (bool, error) {
		ok, observed := fn()
		last = observed
		return ok, nil
	})
	if err != nil {
		t.Fatalf("%s: not reached within %s\nlast observed: %s", what, timeout, last)
	}
}

// consistently asserts fn holds for the whole window; the only way to show
// that something did not happen.
func consistently(t *testing.T, window time.Duration, what string, fn func() (ok bool, observed string)) {
	t.Helper()
	deadline := time.Now().Add(window)
	for time.Now().Before(deadline) {
		if ok, observed := fn(); !ok {
			t.Fatalf("%s: violated within %s\nobserved: %s", what, window, observed)
		}
		time.Sleep(pollInterval)
	}
}

// --- ZoneRoutes ---------------------------------------------------------

type upstream struct {
	address string
	port    int32
}

func newRoute(name string, zones []string, ups ...upstream) *v1alpha1.ZoneRoute {
	zr := &v1alpha1.ZoneRoute{ObjectMeta: metav1.ObjectMeta{Name: name}}
	zr.Spec.Zones = zones
	for _, u := range ups {
		zr.Spec.Upstreams = append(zr.Spec.Upstreams, v1alpha1.Upstream{Address: u.address, Port: u.port})
	}
	return zr
}

// createRoute creates a ZoneRoute and deletes it when the test ends, waiting
// until the fragment no longer mentions it so the next scenario starts
// clean. The returned object carries the server-populated generation.
func createRoute(t *testing.T, name string, zones []string, ups ...upstream) *v1alpha1.ZoneRoute {
	t.Helper()
	zr := newRoute(name, zones, ups...)
	if err := kube.Create(ctx, zr); err != nil {
		t.Fatalf("creating ZoneRoute %s: %v", name, err)
	}
	t.Cleanup(func() { deleteRoute(t, name) })
	return zr
}

func deleteRoute(t *testing.T, name string) {
	t.Helper()
	err := kube.Delete(ctx, &v1alpha1.ZoneRoute{ObjectMeta: metav1.ObjectMeta{Name: name}})
	if err != nil && !apierrors.IsNotFound(err) {
		t.Fatalf("deleting ZoneRoute %s: %v", name, err)
	}
	marker := "# zoneroute: " + name + " ("
	eventually(t, objectTimeout, "fragment no longer contains "+name, func() (bool, string) {
		f, _, ok := fragment()
		return !ok || !strings.Contains(f, marker), f
	})
}

func getRoute(t *testing.T, name string) *v1alpha1.ZoneRoute {
	t.Helper()
	var zr v1alpha1.ZoneRoute
	if err := kube.Get(ctx, types.NamespacedName{Name: name}, &zr); err != nil {
		t.Fatalf("reading ZoneRoute %s: %v", name, err)
	}
	return &zr
}

// waitCondition waits for the condition to have the given status and reason
// for the object's current generation, and returns it.
func waitCondition(t *testing.T, name, condType string, status metav1.ConditionStatus, reason string) metav1.Condition {
	t.Helper()
	var got metav1.Condition
	eventually(t, objectTimeout, fmt.Sprintf("ZoneRoute %s %s=%s/%s", name, condType, status, reason), func() (bool, string) {
		var zr v1alpha1.ZoneRoute
		if err := kube.Get(ctx, types.NamespacedName{Name: name}, &zr); err != nil {
			return false, err.Error()
		}
		c := meta.FindStatusCondition(zr.Status.Conditions, condType)
		if c == nil {
			return false, "condition absent"
		}
		observed := fmt.Sprintf("%s=%s/%s observedGeneration=%d/%d generation=%d message=%q",
			c.Type, c.Status, c.Reason, c.ObservedGeneration, zr.Status.ObservedGeneration, zr.Generation, c.Message)
		if c.Status == status && c.Reason == reason && c.ObservedGeneration == zr.Generation && zr.Status.ObservedGeneration == zr.Generation {
			got = *c
			return true, observed
		}
		return false, observed
	})
	return got
}

// --- ConfigMaps ---------------------------------------------------------

func getConfigMap(namespace, name string) (*corev1.ConfigMap, bool) {
	var cm corev1.ConfigMap
	err := kube.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, &cm)
	if apierrors.IsNotFound(err) {
		return nil, false
	}
	if err != nil {
		fail(fmt.Errorf("reading ConfigMap %s/%s: %w", namespace, name, err))
	}
	return &cm, true
}

// fragment returns data["zoneroute.server"] of coredns-custom, the
// ConfigMap's resourceVersion, and whether the ConfigMap exists.
func fragment() (content, resourceVersion string, exists bool) {
	cm, ok := getConfigMap(coreDNSNamespace, "coredns-custom")
	if !ok {
		return "", "", false
	}
	return cm.Data[fragmentKey], cm.ResourceVersion, true
}

// expectFragment is what the controller must publish for exactly these
// routes: the rendering contract, computed from the live objects.
func expectFragment(t *testing.T, routes ...*v1alpha1.ZoneRoute) string {
	t.Helper()
	var in []render.Route
	for _, zr := range routes {
		r := render.Route{Name: zr.Name, Generation: zr.Generation, Zones: zr.Spec.Zones}
		for _, u := range zr.Spec.Upstreams {
			r.Upstreams = append(r.Upstreams, render.Upstream{Address: u.Address, Port: u.Port})
		}
		in = append(in, r)
	}
	out, err := render.Render(in)
	if err != nil {
		t.Fatalf("rendering expected fragment: %v", err)
	}
	return string(out)
}

func waitFragment(t *testing.T, want string) {
	t.Helper()
	eventually(t, objectTimeout, "zoneroute.server has the expected content", func() (bool, string) {
		got, _, ok := fragment()
		if !ok {
			return false, "coredns-custom does not exist"
		}
		return got == want, "--- got ---\n" + got + "--- want ---\n" + want
	})
}

// patchConfigMapData sets (or, with a nil value, removes) keys of a
// ConfigMap's data with a JSON merge patch, the same way an operator would.
func patchConfigMapData(t *testing.T, namespace, name string, data map[string]*string) *corev1.ConfigMap {
	t.Helper()
	body, err := json.Marshal(map[string]any{"data": data})
	if err != nil {
		t.Fatal(err)
	}
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name}}
	if err := kube.Patch(ctx, cm, client.RawPatch(types.MergePatchType, body)); err != nil {
		t.Fatalf("patching ConfigMap %s/%s: %v", namespace, name, err)
	}
	return cm
}

// setCustomKey writes a key into coredns-custom and removes it when the
// test ends.
func setCustomKey(t *testing.T, key, value string) *corev1.ConfigMap {
	t.Helper()
	t.Cleanup(func() { deleteCustomKey(t, key) })
	return patchConfigMapData(t, coreDNSNamespace, "coredns-custom", map[string]*string{key: &value})
}

func deleteCustomKey(t *testing.T, key string) {
	t.Helper()
	if _, ok := getConfigMap(coreDNSNamespace, "coredns-custom"); !ok {
		return
	}
	patchConfigMapData(t, coreDNSNamespace, "coredns-custom", map[string]*string{key: nil})
}

func corefile(t *testing.T) string {
	t.Helper()
	cm, ok := getConfigMap(coreDNSNamespace, "coredns")
	if !ok {
		t.Fatal("kube-system/coredns does not exist")
	}
	return cm.Data["Corefile"]
}

func setCorefile(t *testing.T, text string) {
	t.Helper()
	patchConfigMapData(t, coreDNSNamespace, "coredns", map[string]*string{"Corefile": &text})
}

// editCorefile applies mutate to the live Corefile and restores the previous
// text when the test ends. CoreDNS picks the change up through its reload
// plugin; no pod restart is involved.
func editCorefile(t *testing.T, mutate func(string) string) (previous string) {
	t.Helper()
	previous = corefile(t)
	t.Cleanup(func() { setCorefile(t, previous) })
	// A Corefile that does not end in a newline would make an appended block
	// land on the last line, which CoreDNS then refuses to parse. Whoever
	// wrote it last is not this test's problem.
	text := previous
	if !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	setCorefile(t, mutate(text))
	return previous
}

func withoutImport(text string) string {
	var kept []string
	for _, line := range strings.Split(text, "\n") {
		if strings.TrimSpace(line) != importLine {
			kept = append(kept, line)
		}
	}
	return strings.Join(kept, "\n")
}

// --- DNS ----------------------------------------------------------------

// dig resolves name from the fixture pod. With an empty server it goes
// through cluster DNS exactly like a workload would; otherwise it asks that
// server directly. It returns the answer, or "" for NXDOMAIN and errors.
func dig(t *testing.T, name string, tcp bool, server string) string {
	t.Helper()
	args := []string{"-n", fixtureNamespace, "exec", "dig", "--", "dig", "+short", "+time=2", "+tries=1"}
	if tcp {
		args = append(args, "+tcp")
	}
	if server != "" {
		args = append(args, "@"+server)
	}
	args = append(args, name)
	out, err := exec.Command("kubectl", args...).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// coreDNSPodIPs lists the CoreDNS replicas. Each one reloads on its own
// schedule, so a DNS assertion holds only when every replica agrees.
func coreDNSPodIPs(t *testing.T) []string {
	t.Helper()
	var pods corev1.PodList
	if err := kube.List(ctx, &pods, client.InNamespace(coreDNSNamespace), client.MatchingLabels{"k8s-app": "kube-dns"}); err != nil {
		t.Fatalf("listing CoreDNS pods: %v", err)
	}
	var ips []string
	for _, p := range pods.Items {
		if p.Status.PodIP != "" && p.DeletionTimestamp == nil {
			ips = append(ips, p.Status.PodIP)
		}
	}
	if len(ips) == 0 {
		t.Fatal("no CoreDNS pods")
	}
	return ips
}

// resolveEverywhere queries the Service path and every CoreDNS replica and
// returns the answers, keyed by where they came from.
func resolveEverywhere(t *testing.T, name string, tcp bool) map[string]string {
	t.Helper()
	answers := map[string]string{"service": dig(t, name, tcp, "")}
	for _, ip := range coreDNSPodIPs(t) {
		answers[ip] = dig(t, name, tcp, ip)
	}
	return answers
}

func waitDNSOver(t *testing.T, name, want string, tcp bool) {
	t.Helper()
	proto := "udp"
	if tcp {
		proto = "tcp"
	}
	eventually(t, dnsTimeout, fmt.Sprintf("%s resolves to %s over %s on every replica", name, want, proto), func() (bool, string) {
		answers := resolveEverywhere(t, name, tcp)
		for _, got := range answers {
			if got != want {
				return false, fmt.Sprint(answers)
			}
		}
		return true, fmt.Sprint(answers)
	})
}

func waitDNS(t *testing.T, name, want string) {
	t.Helper()
	waitDNSOver(t, name, want, false)
}

func waitDNSNot(t *testing.T, name, not string) {
	t.Helper()
	eventually(t, dnsTimeout, fmt.Sprintf("%s no longer resolves to %s on any replica", name, not), func() (bool, string) {
		answers := resolveEverywhere(t, name, false)
		for _, got := range answers {
			if got == not {
				return false, fmt.Sprint(answers)
			}
		}
		return true, fmt.Sprint(answers)
	})
}

// --- Controller ---------------------------------------------------------

func controllerPods(t *testing.T) []corev1.Pod {
	t.Helper()
	var pods corev1.PodList
	if err := kube.List(ctx, &pods, client.InNamespace(controllerNamespace), client.MatchingLabels{"app.kubernetes.io/name": "zoneroute"}); err != nil {
		t.Fatalf("listing controller pods: %v", err)
	}
	return pods.Items
}

func podReady(p corev1.Pod) bool {
	for _, c := range p.Status.Conditions {
		if c.Type == corev1.PodReady && c.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

// restartController deletes the controller pod and waits for a new one to
// be Ready with the old one gone.
func restartController(t *testing.T) {
	t.Helper()
	old := map[string]bool{}
	for _, p := range controllerPods(t) {
		old[p.Name] = true
		if err := kube.Delete(ctx, &p); err != nil && !apierrors.IsNotFound(err) {
			t.Fatalf("deleting pod %s: %v", p.Name, err)
		}
	}
	eventually(t, rolloutTimeout, "controller pod replaced and Ready", func() (bool, string) {
		var names []string
		ready, stale := 0, 0
		for _, p := range controllerPods(t) {
			names = append(names, fmt.Sprintf("%s(%s)", p.Name, p.Status.Phase))
			switch {
			case old[p.Name]:
				stale++
			case podReady(p):
				ready++
			}
		}
		return stale == 0 && ready == 1, strings.Join(names, ", ")
	})
}

// canI asks the API server whether the controller's ServiceAccount may
// perform the action, with no impersonation involved.
func canI(t *testing.T, verb, group, resource, subresource, namespace, name string) bool {
	t.Helper()
	sar := &authorizationv1.SubjectAccessReview{Spec: authorizationv1.SubjectAccessReviewSpec{
		User:   "system:serviceaccount:" + controllerNamespace + ":zoneroute-controller",
		Groups: []string{"system:serviceaccounts", "system:serviceaccounts:" + controllerNamespace, "system:authenticated"},
		ResourceAttributes: &authorizationv1.ResourceAttributes{
			Verb: verb, Group: group, Resource: resource, Subresource: subresource, Namespace: namespace, Name: name,
		},
	}}
	if err := kube.Create(ctx, sar); err != nil {
		t.Fatalf("SubjectAccessReview: %v", err)
	}
	return sar.Status.Allowed
}
