package controller

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/mihnk/zoneroute/api/v1alpha1"
	"github.com/mihnk/zoneroute/internal/corefile"
	"github.com/mihnk/zoneroute/internal/render"
)

const (
	corefileUnwired = ".:53 {\n    forward . /etc/resolv.conf\n}\n"
	corefileWired   = corefileUnwired + "import custom/*.server\n"
)

var t0 = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// counters records API writes so tests can assert that nothing unnecessary
// was written.
type counters struct {
	patches       int
	statusUpdates int
	patchErr      error
}

type fixture struct {
	t      *testing.T
	client client.Client
	rec    *Reconciler
	count  *counters
}

func newFixture(t *testing.T, objs ...client.Object) *fixture {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	count := &counters{}
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&v1alpha1.ZoneRoute{}).
		WithObjects(objs...).
		WithInterceptorFuncs(interceptor.Funcs{
			Patch: func(ctx context.Context, cl client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
				count.patches++
				if count.patchErr != nil {
					return count.patchErr
				}
				return cl.Patch(ctx, obj, patch, opts...)
			},
			SubResourceUpdate: func(ctx context.Context, cl client.Client, subResourceName string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
				count.statusUpdates++
				return cl.SubResource(subResourceName).Update(ctx, obj, opts...)
			},
		}).
		Build()
	return &fixture{t: t, client: c, rec: &Reconciler{Client: c, ClusterDomain: "cluster.local."}, count: count}
}

func (f *fixture) reconcile() error {
	f.t.Helper()
	_, err := f.rec.Reconcile(context.Background(), globalRequest)
	return err
}

func (f *fixture) mustReconcile() {
	f.t.Helper()
	if err := f.reconcile(); err != nil {
		f.t.Fatalf("Reconcile: %v", err)
	}
}

func (f *fixture) fragment() (string, bool) {
	f.t.Helper()
	var cm corev1.ConfigMap
	if err := f.client.Get(context.Background(), types.NamespacedName{Namespace: CoreDNSNamespace, Name: integrationConfigMap}, &cm); err != nil {
		f.t.Fatalf("get coredns-custom: %v", err)
	}
	v, ok := cm.Data[corefile.ManagedKey]
	return v, ok
}

func (f *fixture) custom() *corev1.ConfigMap {
	f.t.Helper()
	var cm corev1.ConfigMap
	if err := f.client.Get(context.Background(), types.NamespacedName{Namespace: CoreDNSNamespace, Name: integrationConfigMap}, &cm); err != nil {
		f.t.Fatalf("get coredns-custom: %v", err)
	}
	return &cm
}

func (f *fixture) route(name string) *v1alpha1.ZoneRoute {
	f.t.Helper()
	var zr v1alpha1.ZoneRoute
	if err := f.client.Get(context.Background(), types.NamespacedName{Name: name}, &zr); err != nil {
		f.t.Fatalf("get ZoneRoute %s: %v", name, err)
	}
	return &zr
}

func (f *fixture) condition(name, condType string) metav1.Condition {
	f.t.Helper()
	zr := f.route(name)
	c := meta.FindStatusCondition(zr.Status.Conditions, condType)
	if c == nil {
		f.t.Fatalf("ZoneRoute %s has no %s condition: %+v", name, condType, zr.Status.Conditions)
	}
	return *c
}

func (f *fixture) expect(name, condType string, status metav1.ConditionStatus, reason string) metav1.Condition {
	f.t.Helper()
	c := f.condition(name, condType)
	if c.Status != status || c.Reason != reason {
		f.t.Errorf("ZoneRoute %s %s = %s/%s, want %s/%s (message: %q)", name, condType, c.Status, c.Reason, status, reason, c.Message)
	}
	return c
}

func corednsCM(corefileText string) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Namespace: CoreDNSNamespace, Name: corefileConfigMap},
		Data:       map[string]string{corefileKey: corefileText},
	}
}

func customCM(data map[string]string) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Namespace: CoreDNSNamespace, Name: integrationConfigMap},
		Data:       data,
	}
}

func zoneRoute(name string, created time.Time, generation int64, zones []string, upstreams ...string) *v1alpha1.ZoneRoute {
	ups := make([]v1alpha1.Upstream, 0, len(upstreams))
	for _, u := range upstreams {
		ups = append(ups, v1alpha1.Upstream{Address: u, Port: 53})
	}
	return &v1alpha1.ZoneRoute{
		ObjectMeta: metav1.ObjectMeta{Name: name, CreationTimestamp: metav1.NewTime(created), Generation: generation},
		Spec:       v1alpha1.ZoneRouteSpec{Zones: zones, Upstreams: ups},
	}
}

func expectedFragment(t *testing.T, routes ...render.Route) string {
	t.Helper()
	out, err := render.Render(routes)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

func TestMapToGlobal(t *testing.T) {
	for _, name := range []string{"a", "corporate", "zzz"} {
		got := mapToGlobal(context.Background(), zoneRoute(name, t0, 1, []string{"x.test"}, "10.0.0.1"))
		if len(got) != 1 || got[0] != globalRequest {
			t.Errorf("mapToGlobal(%s) = %v, want [%v]", name, got, globalRequest)
		}
	}
}

func TestAcceptedRouteIsRenderedAndPublished(t *testing.T) {
	f := newFixture(t,
		corednsCM(corefileWired),
		customCM(map[string]string{}),
		zoneRoute("corporate", t0, 3, []string{"company.local", "corp.internal"}, "10.10.10.53", "10.10.10.54"),
	)
	f.mustReconcile()

	got, _ := f.fragment()
	want := expectedFragment(t, render.Route{
		Name: "corporate", Generation: 3, Zones: []string{"company.local", "corp.internal"},
		Upstreams: []render.Upstream{{Address: "10.10.10.53", Port: 53}, {Address: "10.10.10.54", Port: 53}},
	})
	if got != want {
		t.Errorf("fragment =\n%s\nwant\n%s", got, want)
	}
	f.expect("corporate", v1alpha1.ConditionAccepted, metav1.ConditionTrue, v1alpha1.ReasonAccepted)
	f.expect("corporate", v1alpha1.ConditionPublished, metav1.ConditionTrue, v1alpha1.ReasonPublished)
}

func TestConflictingRoutesOnlyWinnerIsRendered(t *testing.T) {
	f := newFixture(t,
		corednsCM(corefileWired),
		customCM(map[string]string{}),
		zoneRoute("older", t0, 1, []string{"dup.test"}, "10.0.0.1"),
		zoneRoute("newer", t0.Add(time.Second), 1, []string{"dup.test"}, "10.0.0.2"),
	)
	f.mustReconcile()

	got, _ := f.fragment()
	if !strings.Contains(got, "# zoneroute: older") || strings.Contains(got, "# zoneroute: newer") {
		t.Errorf("fragment should contain only the winner:\n%s", got)
	}
	f.expect("older", v1alpha1.ConditionAccepted, metav1.ConditionTrue, v1alpha1.ReasonAccepted)
	c := f.expect("newer", v1alpha1.ConditionAccepted, metav1.ConditionFalse, v1alpha1.ReasonZoneConflict)
	if !strings.Contains(c.Message, "older") {
		t.Errorf("conflict message should name the winner: %q", c.Message)
	}
	f.expect("newer", v1alpha1.ConditionPublished, metav1.ConditionFalse, v1alpha1.ReasonNotAccepted)
}

func TestReservedRouteIsRejected(t *testing.T) {
	f := newFixture(t,
		corednsCM(corefileWired),
		customCM(map[string]string{}),
		zoneRoute("r", t0, 1, []string{"svc.cluster.local"}, "10.0.0.1"),
	)
	f.mustReconcile()

	c := f.expect("r", v1alpha1.ConditionAccepted, metav1.ConditionFalse, v1alpha1.ReasonReservedZone)
	if !strings.Contains(c.Message, "svc.cluster.local.") {
		t.Errorf("message should name the zone: %q", c.Message)
	}
	got, _ := f.fragment()
	if strings.Contains(got, "svc.cluster.local") {
		t.Errorf("reserved zone must not be rendered:\n%s", got)
	}
}

func TestCoreDNSOwnedRouteIsRejected(t *testing.T) {
	f := newFixture(t,
		corednsCM("example.com {\n    forward . 10.9.9.9\n}\n"+corefileWired),
		customCM(map[string]string{}),
		zoneRoute("r", t0, 1, []string{"example.com"}, "10.0.0.1"),
	)
	f.mustReconcile()

	c := f.expect("r", v1alpha1.ConditionAccepted, metav1.ConditionFalse, v1alpha1.ReasonZoneOwnedByCoreDNS)
	if !strings.Contains(c.Message, "Corefile") {
		t.Errorf("message should name the source: %q", c.Message)
	}
}

func TestUnresolvedImportYieldsAcceptedWithUnresolvedImports(t *testing.T) {
	f := newFixture(t,
		corednsCM("import /etc/coredns/extra.conf\n"+corefileWired),
		customCM(map[string]string{}),
		zoneRoute("r", t0, 1, []string{"x.test"}, "10.0.0.1"),
	)
	f.mustReconcile()

	c := f.expect("r", v1alpha1.ConditionAccepted, metav1.ConditionTrue, v1alpha1.ReasonAcceptedWithUnresolvedImports)
	if !strings.Contains(c.Message, "Corefile:1 /etc/coredns/extra.conf") {
		t.Errorf("message should list the unresolved import: %q", c.Message)
	}
	f.expect("r", v1alpha1.ConditionPublished, metav1.ConditionTrue, v1alpha1.ReasonPublished)
}

func TestMissingIntegrationConfigMap(t *testing.T) {
	f := newFixture(t,
		corednsCM(corefileWired),
		zoneRoute("r", t0, 1, []string{"x.test"}, "10.0.0.1"),
	)
	f.mustReconcile()

	f.expect("r", v1alpha1.ConditionAccepted, metav1.ConditionTrue, v1alpha1.ReasonAccepted)
	f.expect("r", v1alpha1.ConditionPublished, metav1.ConditionFalse, v1alpha1.ReasonIntegrationConfigMissing)

	var cm corev1.ConfigMap
	err := f.client.Get(context.Background(), types.NamespacedName{Namespace: CoreDNSNamespace, Name: integrationConfigMap}, &cm)
	if err == nil {
		t.Errorf("controller must not create coredns-custom")
	}
	if f.count.patches != 0 {
		t.Errorf("patches = %d, want 0", f.count.patches)
	}
}

func TestMissingWiringStillWritesFragment(t *testing.T) {
	f := newFixture(t,
		corednsCM(corefileUnwired),
		customCM(map[string]string{}),
		zoneRoute("r", t0, 1, []string{"x.test"}, "10.0.0.1"),
	)
	f.mustReconcile()

	if got, ok := f.fragment(); !ok || !strings.Contains(got, "x.test {") {
		t.Errorf("fragment should be written even when unwired; got %q", got)
	}
	f.expect("r", v1alpha1.ConditionPublished, metav1.ConditionFalse, v1alpha1.ReasonCoreDNSNotWired)
}

func TestIdenticalFragmentIsNotRewritten(t *testing.T) {
	f := newFixture(t,
		corednsCM(corefileWired),
		customCM(map[string]string{}),
		zoneRoute("r", t0, 1, []string{"x.test"}, "10.0.0.1"),
	)
	f.mustReconcile()
	if f.count.patches != 1 {
		t.Fatalf("first reconcile patches = %d, want 1", f.count.patches)
	}
	f.mustReconcile()
	if f.count.patches != 1 {
		t.Errorf("second reconcile patched again: patches = %d", f.count.patches)
	}
}

func TestPatchPreservesOtherKeys(t *testing.T) {
	f := newFixture(t,
		corednsCM(corefileWired),
		customCM(map[string]string{"user.server": "u.test {\n    forward . 10.1.1.1\n}\n", "log.override": "log\n"}),
		zoneRoute("r", t0, 1, []string{"x.test"}, "10.0.0.1"),
	)
	f.mustReconcile()

	cm := f.custom()
	if cm.Data["user.server"] != "u.test {\n    forward . 10.1.1.1\n}\n" || cm.Data["log.override"] != "log\n" {
		t.Errorf("other keys were modified: %v", cm.Data)
	}
	if _, ok := cm.Data[corefile.ManagedKey]; !ok {
		t.Errorf("managed key missing after publish")
	}
}

func TestPatchFailureYieldsWriteFailedAndError(t *testing.T) {
	f := newFixture(t,
		corednsCM(corefileWired),
		customCM(map[string]string{}),
		zoneRoute("ok", t0, 1, []string{"x.test"}, "10.0.0.1"),
		zoneRoute("bad", t0, 1, []string{"svc.cluster.local"}, "10.0.0.1"),
	)
	f.count.patchErr = errors.New("boom")

	if err := f.reconcile(); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("Reconcile error = %v, want patch failure", err)
	}
	f.expect("ok", v1alpha1.ConditionAccepted, metav1.ConditionTrue, v1alpha1.ReasonAccepted)
	f.expect("ok", v1alpha1.ConditionPublished, metav1.ConditionFalse, v1alpha1.ReasonWriteFailed)
	f.expect("bad", v1alpha1.ConditionPublished, metav1.ConditionFalse, v1alpha1.ReasonNotAccepted)
}

func TestObservedGeneration(t *testing.T) {
	f := newFixture(t,
		corednsCM(corefileWired),
		customCM(map[string]string{}),
		zoneRoute("r", t0, 7, []string{"x.test"}, "10.0.0.1"),
	)
	f.mustReconcile()

	zr := f.route("r")
	if zr.Status.ObservedGeneration != 7 {
		t.Errorf("status.observedGeneration = %d, want 7", zr.Status.ObservedGeneration)
	}
	for _, c := range zr.Status.Conditions {
		if c.ObservedGeneration != 7 {
			t.Errorf("condition %s observedGeneration = %d, want 7", c.Type, c.ObservedGeneration)
		}
	}
}

func TestRepeatedReconcileWritesNoStatus(t *testing.T) {
	f := newFixture(t,
		corednsCM(corefileWired),
		customCM(map[string]string{}),
		zoneRoute("a", t0, 1, []string{"a.test"}, "10.0.0.1"),
		zoneRoute("b", t0, 1, []string{"a.test"}, "10.0.0.2"),
	)
	f.mustReconcile()
	first := f.count.statusUpdates
	if first != 2 {
		t.Fatalf("first reconcile status updates = %d, want 2", first)
	}
	before := f.condition("a", v1alpha1.ConditionPublished).LastTransitionTime

	f.mustReconcile()
	if f.count.statusUpdates != first {
		t.Errorf("second reconcile wrote status: %d updates", f.count.statusUpdates-first)
	}
	if after := f.condition("a", v1alpha1.ConditionPublished).LastTransitionTime; !after.Equal(&before) {
		t.Errorf("LastTransitionTime changed on an unchanged condition")
	}
}

func TestReasonChangeKeepsLastTransitionTime(t *testing.T) {
	f := newFixture(t,
		corednsCM(corefileWired),
		customCM(map[string]string{}),
		zoneRoute("r", t0, 1, []string{"x.test"}, "10.0.0.1"),
	)
	f.mustReconcile()
	before := f.condition("r", v1alpha1.ConditionAccepted)

	// Add an unresolved import: Status stays True, Reason changes.
	cm := corednsCM("import /etc/coredns/extra.conf\n" + corefileWired)
	var existing corev1.ConfigMap
	if err := f.client.Get(context.Background(), client.ObjectKeyFromObject(cm), &existing); err != nil {
		t.Fatal(err)
	}
	existing.Data = cm.Data
	if err := f.client.Update(context.Background(), &existing); err != nil {
		t.Fatal(err)
	}
	f.mustReconcile()

	after := f.expect("r", v1alpha1.ConditionAccepted, metav1.ConditionTrue, v1alpha1.ReasonAcceptedWithUnresolvedImports)
	if !after.LastTransitionTime.Equal(&before.LastTransitionTime) {
		t.Errorf("LastTransitionTime changed although Status did not")
	}
}

func TestDeletionRegeneratesFragment(t *testing.T) {
	f := newFixture(t,
		corednsCM(corefileWired),
		customCM(map[string]string{}),
		zoneRoute("keep", t0, 1, []string{"keep.test"}, "10.0.0.1"),
		zoneRoute("gone", t0, 1, []string{"gone.test"}, "10.0.0.2"),
	)
	f.mustReconcile()
	if got, _ := f.fragment(); !strings.Contains(got, "gone.test {") {
		t.Fatalf("fragment should contain gone.test before deletion:\n%s", got)
	}

	if err := f.client.Delete(context.Background(), f.route("gone")); err != nil {
		t.Fatal(err)
	}
	f.mustReconcile()

	got, _ := f.fragment()
	if strings.Contains(got, "gone.test") || !strings.Contains(got, "keep.test {") {
		t.Errorf("fragment after deletion:\n%s", got)
	}
}

func TestResultIsIndependentOfListOrder(t *testing.T) {
	routes := func() []client.Object {
		return []client.Object{
			zoneRoute("c", t0, 1, []string{"c.test"}, "10.0.0.3"),
			zoneRoute("a", t0, 1, []string{"a.test"}, "10.0.0.1"),
			zoneRoute("b", t0.Add(time.Second), 1, []string{"a.test", "b.test"}, "10.0.0.2"),
		}
	}
	forward := newFixture(t, append([]client.Object{corednsCM(corefileWired), customCM(map[string]string{})}, routes()...)...)
	rs := routes()
	rs[0], rs[2] = rs[2], rs[0]
	reversed := newFixture(t, append([]client.Object{corednsCM(corefileWired), customCM(map[string]string{})}, rs...)...)

	forward.mustReconcile()
	reversed.mustReconcile()

	f1, _ := forward.fragment()
	f2, _ := reversed.fragment()
	if f1 != f2 {
		t.Errorf("fragments differ by list order:\n%s\n---\n%s", f1, f2)
	}
	for _, name := range []string{"a", "b", "c"} {
		c1, c2 := forward.condition(name, v1alpha1.ConditionAccepted), reversed.condition(name, v1alpha1.ConditionAccepted)
		if c1.Status != c2.Status || c1.Reason != c2.Reason || c1.Message != c2.Message {
			t.Errorf("ZoneRoute %s Accepted differs by list order", name)
		}
	}
}

func TestMissingCorefileFailsClosed(t *testing.T) {
	t.Run("coredns ConfigMap absent", func(t *testing.T) {
		f := newFixture(t,
			customCM(map[string]string{}),
			zoneRoute("r", t0, 1, []string{"x.test"}, "10.0.0.1"),
		)
		if err := f.reconcile(); err == nil {
			t.Fatal("Reconcile: want error, got nil")
		}
		if _, ok := f.fragment(); ok {
			t.Errorf("fragment must not be written")
		}
		if len(f.route("r").Status.Conditions) != 0 {
			t.Errorf("status must not be written")
		}
	})

	t.Run("Corefile key absent", func(t *testing.T) {
		f := newFixture(t,
			&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: CoreDNSNamespace, Name: corefileConfigMap}, Data: map[string]string{"other": "x"}},
			customCM(map[string]string{}),
			zoneRoute("r", t0, 1, []string{"x.test"}, "10.0.0.1"),
		)
		if err := f.reconcile(); err == nil {
			t.Fatal("Reconcile: want error, got nil")
		}
		if f.count.patches != 0 || f.count.statusUpdates != 0 {
			t.Errorf("writes = patches %d, status %d; want none", f.count.patches, f.count.statusUpdates)
		}
	})
}

func TestClusterDomain(t *testing.T) {
	valid := map[string]string{
		"cluster.local":    "cluster.local.",
		" Cluster.Local. ": "cluster.local.",
		"k8s.example.org":  "k8s.example.org.",
	}
	for in, want := range valid {
		got, err := ClusterDomain(in)
		if err != nil || got != want {
			t.Errorf("ClusterDomain(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"", "   ", ".", "-bad.local", "bad_domain", "a..b", strings.Repeat("a", 254)} {
		if _, err := ClusterDomain(in); err == nil {
			t.Errorf("ClusterDomain(%q): want error, got nil", in)
		}
	}
}
