// Package config holds the static checks for the generated install bundle.
// They decode install.yaml into typed objects and pin the permission set,
// the inventory and the pod hardening, so a manifest edit that widens any
// of them fails `go test`.
package config

import (
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"reflect"
	"slices"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
)

const (
	bundlePath = "../install.yaml"
	crdPath    = "crd/dns.mihnk.org_zoneroutes.yaml"

	namespace      = "zoneroute-system"
	serviceAccount = "zoneroute-controller"
	image          = "ghcr.io/mihnk/zoneroute:dev"
	probePort      = 8081
)

func loadDocs(t *testing.T, path string) []unstructured.Unstructured {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	var docs []unstructured.Unstructured
	dec := utilyaml.NewYAMLOrJSONDecoder(f, 4096)
	for {
		var u unstructured.Unstructured
		err := dec.Decode(&u)
		if errors.Is(err, io.EOF) {
			return docs
		}
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		if len(u.Object) == 0 {
			continue
		}
		docs = append(docs, u)
	}
}

func bundle(t *testing.T) []unstructured.Unstructured {
	t.Helper()
	return loadDocs(t, bundlePath)
}

func decode[T any](t *testing.T, u unstructured.Unstructured) T {
	t.Helper()
	var out T
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, &out); err != nil {
		t.Fatalf("decoding %s %s: %v", u.GetKind(), u.GetName(), err)
	}
	return out
}

func ofKind[T any](t *testing.T, docs []unstructured.Unstructured, kind string) []T {
	t.Helper()
	var out []T
	for _, u := range docs {
		if u.GetKind() == kind {
			out = append(out, decode[T](t, u))
		}
	}
	return out
}

func TestBundleInventory(t *testing.T) {
	got := map[string]int{}
	for _, u := range bundle(t) {
		got[u.GetKind()]++
	}
	want := map[string]int{
		"Namespace":                1,
		"CustomResourceDefinition": 1,
		"ServiceAccount":           1,
		"ClusterRole":              1,
		"ClusterRoleBinding":       1,
		"Role":                     2,
		"RoleBinding":              2,
		"Deployment":               1,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("inventory = %v, want %v", got, want)
	}
}

func TestBundleCRDMatchesSource(t *testing.T) {
	var inBundle []unstructured.Unstructured
	for _, u := range bundle(t) {
		if u.GetKind() == "CustomResourceDefinition" {
			inBundle = append(inBundle, u)
		}
	}
	if len(inBundle) != 1 || inBundle[0].GetName() != "zoneroutes.dns.mihnk.org" {
		t.Fatalf("want exactly one ZoneRoute CRD, got %d", len(inBundle))
	}
	source := loadDocs(t, crdPath)
	if len(source) != 1 {
		t.Fatalf("%s holds %d documents, want 1", crdPath, len(source))
	}
	// Semantic comparison: kustomize may reserialize, the content must not move.
	if !reflect.DeepEqual(inBundle[0].Object, source[0].Object) {
		t.Error("CRD in install.yaml differs from config/crd")
	}
}

func TestNamespace(t *testing.T) {
	ns := ofKind[corev1.Namespace](t, bundle(t), "Namespace")
	if len(ns) != 1 || ns[0].Name != namespace {
		t.Fatalf("namespaces = %v", ns)
	}
	if got := ns[0].Labels["pod-security.kubernetes.io/enforce"]; got != "restricted" {
		t.Errorf("pod-security enforce = %q, want restricted", got)
	}
}

// Every namespaced object is in zoneroute-system, except the CoreDNS Role
// and its binding, which must be in kube-system.
func TestObjectNamespaces(t *testing.T) {
	for _, u := range bundle(t) {
		want := namespace
		switch u.GetKind() {
		case "Namespace", "CustomResourceDefinition", "ClusterRole", "ClusterRoleBinding":
			want = ""
		case "Role", "RoleBinding":
			if u.GetName() == "zoneroute-coredns" {
				want = "kube-system"
			}
		}
		if got := u.GetNamespace(); got != want {
			t.Errorf("%s %s: namespace %q, want %q", u.GetKind(), u.GetName(), got, want)
		}
	}
}

func rule(group, resource string, verbs ...string) rbacv1.PolicyRule {
	return rbacv1.PolicyRule{APIGroups: []string{group}, Resources: []string{resource}, Verbs: verbs}
}

func namedRule(group, resource, name string, verbs ...string) rbacv1.PolicyRule {
	r := rule(group, resource, verbs...)
	r.ResourceNames = []string{name}
	return r
}

// The exact permission set. Anything else is a widening and fails here.
func TestRBACRulesAreExact(t *testing.T) {
	docs := bundle(t)

	clusterRoles := ofKind[rbacv1.ClusterRole](t, docs, "ClusterRole")
	if len(clusterRoles) != 1 || clusterRoles[0].Name != "zoneroute-controller" {
		t.Fatalf("cluster roles = %v", clusterRoles)
	}
	wantCluster := []rbacv1.PolicyRule{
		rule("dns.mihnk.org", "zoneroutes", "list", "watch"),
		rule("dns.mihnk.org", "zoneroutes/status", "update"),
	}
	if !reflect.DeepEqual(clusterRoles[0].Rules, wantCluster) {
		t.Errorf("ClusterRole rules = %+v\nwant %+v", clusterRoles[0].Rules, wantCluster)
	}

	wantRoles := map[string]struct {
		namespace string
		rules     []rbacv1.PolicyRule
	}{
		"zoneroute-coredns": {"kube-system", []rbacv1.PolicyRule{
			rule("", "configmaps", "list", "watch"),
			namedRule("", "configmaps", "coredns-custom", "patch"),
		}},
		"zoneroute-leader-election": {namespace, []rbacv1.PolicyRule{
			rule("coordination.k8s.io", "leases", "create"),
			namedRule("coordination.k8s.io", "leases", "zoneroute.dns.mihnk.org", "get", "update"),
		}},
	}
	roles := ofKind[rbacv1.Role](t, docs, "Role")
	if len(roles) != len(wantRoles) {
		t.Fatalf("got %d roles, want %d", len(roles), len(wantRoles))
	}
	for _, r := range roles {
		want, ok := wantRoles[r.Name]
		if !ok {
			t.Errorf("unexpected Role %s", r.Name)
			continue
		}
		if r.Namespace != want.namespace {
			t.Errorf("Role %s: namespace %q, want %q", r.Name, r.Namespace, want.namespace)
		}
		if !reflect.DeepEqual(r.Rules, want.rules) {
			t.Errorf("Role %s rules = %+v\nwant %+v", r.Name, r.Rules, want.rules)
		}
	}
}

// Invariants that must hold however the rules are arranged.
func TestRBACInvariants(t *testing.T) {
	docs := bundle(t)

	type scoped struct {
		owner     string
		namespace string
		rules     []rbacv1.PolicyRule
	}
	var all []scoped
	for _, cr := range ofKind[rbacv1.ClusterRole](t, docs, "ClusterRole") {
		all = append(all, scoped{"ClusterRole/" + cr.Name, "", cr.Rules})
	}
	for _, r := range ofKind[rbacv1.Role](t, docs, "Role") {
		all = append(all, scoped{"Role/" + r.Name, r.Namespace, r.Rules})
	}

	allowed := map[[2]string]string{ // (group, resource) -> namespace it may appear in ("" = ClusterRole)
		{"dns.mihnk.org", "zoneroutes"}:        "",
		{"dns.mihnk.org", "zoneroutes/status"}: "",
		{"", "configmaps"}:                     "kube-system",
		{"coordination.k8s.io", "leases"}:      namespace,
	}

	for _, s := range all {
		for _, r := range s.rules {
			if slices.Contains(r.Verbs, "*") || slices.Contains(r.Resources, "*") || slices.Contains(r.APIGroups, "*") || len(r.NonResourceURLs) > 0 {
				t.Errorf("%s: wildcard or non-resource rule %+v", s.owner, r)
			}
			for _, g := range r.APIGroups {
				for _, res := range r.Resources {
					ns, ok := allowed[[2]string{g, res}]
					if !ok {
						t.Errorf("%s: permission on %s/%s is not allowed", s.owner, g, res)
						continue
					}
					if ns != s.namespace {
						t.Errorf("%s: %s/%s must be granted in namespace %q, found in %q", s.owner, g, res, ns, s.namespace)
					}
					switch res {
					case "configmaps":
						for _, v := range r.Verbs {
							if v != "list" && v != "watch" && v != "patch" {
								t.Errorf("%s: configmaps verb %q not allowed", s.owner, v)
							}
							if v == "patch" && !reflect.DeepEqual(r.ResourceNames, []string{"coredns-custom"}) {
								t.Errorf("%s: configmaps patch must be limited to coredns-custom, got %v", s.owner, r.ResourceNames)
							}
						}
					case "zoneroutes":
						for _, v := range r.Verbs {
							if v != "list" && v != "watch" {
								t.Errorf("%s: zoneroutes verb %q not allowed (spec is read-only)", s.owner, v)
							}
						}
					case "zoneroutes/status":
						if !reflect.DeepEqual(r.Verbs, []string{"update"}) {
							t.Errorf("%s: zoneroutes/status verbs = %v, want [update]", s.owner, r.Verbs)
						}
					}
				}
			}
		}
	}
}

func TestBindingsTargetTheServiceAccount(t *testing.T) {
	docs := bundle(t)
	sas := ofKind[corev1.ServiceAccount](t, docs, "ServiceAccount")
	if len(sas) != 1 || sas[0].Name != serviceAccount || sas[0].Namespace != namespace {
		t.Fatalf("service accounts = %v", sas)
	}
	wantSubjects := []rbacv1.Subject{{Kind: "ServiceAccount", Name: serviceAccount, Namespace: namespace}}

	crbs := ofKind[rbacv1.ClusterRoleBinding](t, docs, "ClusterRoleBinding")
	if len(crbs) != 1 {
		t.Fatalf("got %d ClusterRoleBindings", len(crbs))
	}
	if crbs[0].RoleRef != (rbacv1.RoleRef{APIGroup: "rbac.authorization.k8s.io", Kind: "ClusterRole", Name: "zoneroute-controller"}) {
		t.Errorf("ClusterRoleBinding roleRef = %+v", crbs[0].RoleRef)
	}
	if !reflect.DeepEqual(crbs[0].Subjects, wantSubjects) {
		t.Errorf("ClusterRoleBinding subjects = %+v", crbs[0].Subjects)
	}

	wantRB := map[string]string{"zoneroute-coredns": "kube-system", "zoneroute-leader-election": namespace}
	rbs := ofKind[rbacv1.RoleBinding](t, docs, "RoleBinding")
	if len(rbs) != len(wantRB) {
		t.Fatalf("got %d RoleBindings, want %d", len(rbs), len(wantRB))
	}
	for _, rb := range rbs {
		ns, ok := wantRB[rb.Name]
		if !ok || rb.Namespace != ns {
			t.Errorf("RoleBinding %s in %q unexpected", rb.Name, rb.Namespace)
		}
		// Each binding refers to the Role of the same name in its own namespace.
		if rb.RoleRef != (rbacv1.RoleRef{APIGroup: "rbac.authorization.k8s.io", Kind: "Role", Name: rb.Name}) {
			t.Errorf("RoleBinding %s roleRef = %+v", rb.Name, rb.RoleRef)
		}
		if !reflect.DeepEqual(rb.Subjects, wantSubjects) {
			t.Errorf("RoleBinding %s subjects = %+v", rb.Name, rb.Subjects)
		}
	}
}

func TestDeployment(t *testing.T) {
	deps := ofKind[appsv1.Deployment](t, bundle(t), "Deployment")
	if len(deps) != 1 {
		t.Fatalf("got %d Deployments", len(deps))
	}
	d := deps[0]
	if d.Namespace != namespace || d.Name != "zoneroute-controller" {
		t.Errorf("%s/%s", d.Namespace, d.Name)
	}
	if d.Spec.Replicas == nil || *d.Spec.Replicas != 1 {
		t.Errorf("replicas = %v, want 1", d.Spec.Replicas)
	}
	if d.Spec.Strategy.Type == appsv1.RecreateDeploymentStrategyType {
		t.Error("strategy must be RollingUpdate; leader election handles overlap")
	}
	if !reflect.DeepEqual(d.Spec.Selector.MatchLabels, d.Spec.Template.Labels) {
		t.Errorf("selector %v does not match template labels %v", d.Spec.Selector.MatchLabels, d.Spec.Template.Labels)
	}

	pod := d.Spec.Template.Spec
	if pod.ServiceAccountName != serviceAccount {
		t.Errorf("serviceAccountName = %q", pod.ServiceAccountName)
	}
	if pod.HostNetwork || pod.HostPID || pod.HostIPC || len(pod.Volumes) != 0 {
		t.Error("host namespaces and volumes must not be used")
	}
	if pod.TerminationGracePeriodSeconds == nil || *pod.TerminationGracePeriodSeconds != 10 {
		t.Errorf("terminationGracePeriodSeconds = %v", pod.TerminationGracePeriodSeconds)
	}
	wantPodSC := &corev1.PodSecurityContext{
		RunAsNonRoot:   ptr(true),
		RunAsUser:      ptr[int64](65532),
		SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
	}
	if !reflect.DeepEqual(pod.SecurityContext, wantPodSC) {
		t.Errorf("pod securityContext = %+v", pod.SecurityContext)
	}

	if len(pod.Containers) != 1 || len(pod.InitContainers) != 0 {
		t.Fatalf("want exactly one container, got %d (+%d init)", len(pod.Containers), len(pod.InitContainers))
	}
	c := pod.Containers[0]
	if c.Name != "controller" || c.Image != image {
		t.Errorf("container %s image %s", c.Name, c.Image)
	}
	if len(c.Command) != 0 {
		t.Errorf("command must come from the image, got %v", c.Command)
	}
	wantArgs := []string{
		"--cluster-domain=cluster.local",
		"--leader-elect=true",
		"--health-probe-bind-address=:8081",
		"--metrics-bind-address=0",
	}
	if !reflect.DeepEqual(c.Args, wantArgs) {
		t.Errorf("args = %v, want %v", c.Args, wantArgs)
	}
	wantSC := &corev1.SecurityContext{
		AllowPrivilegeEscalation: ptr(false),
		ReadOnlyRootFilesystem:   ptr(true),
		Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
	}
	if !reflect.DeepEqual(c.SecurityContext, wantSC) {
		t.Errorf("container securityContext = %+v", c.SecurityContext)
	}

	if len(c.Ports) != 1 || c.Ports[0].Name != "probes" || c.Ports[0].ContainerPort != probePort {
		t.Errorf("ports = %+v", c.Ports)
	}
	for name, probe := range map[string]*corev1.Probe{"liveness": c.LivenessProbe, "readiness": c.ReadinessProbe} {
		wantPath := map[string]string{"liveness": "/healthz", "readiness": "/readyz"}[name]
		if probe == nil || probe.HTTPGet == nil {
			t.Errorf("%s probe missing or not httpGet", name)
			continue
		}
		if probe.HTTPGet.Path != wantPath || probe.HTTPGet.Port.String() != "probes" {
			t.Errorf("%s probe = %s:%s, want %s:probes", name, probe.HTTPGet.Path, probe.HTTPGet.Port.String(), wantPath)
		}
	}

	if c.Resources.Requests.Cpu().IsZero() || c.Resources.Requests.Memory().IsZero() || c.Resources.Limits.Memory().IsZero() {
		t.Errorf("resources = %+v; want cpu/memory requests and a memory limit", c.Resources)
	}
	if !c.Resources.Limits.Cpu().IsZero() {
		t.Error("no CPU limit: it only throttles")
	}
}

func ptr[T any](v T) *T { return &v }

// The release asset is the development bundle with one difference: the image
// is pinned by digest. Everything else — every object, every permission,
// every security setting — must be byte-identical, so the manifest users
// install is the one the e2e suite validated.
func TestReleaseManifestOnlyRepinsTheImage(t *testing.T) {
	const digest = "sha256:" +
		"1111111111111111111111111111111111111111111111111111111111111111"

	cmd := exec.Command("../hack/release-manifest.sh")
	cmd.Env = append(os.Environ(),
		"DIGEST="+digest,
		"KUSTOMIZE=go run sigs.k8s.io/kustomize/kustomize/v5@v5.8.1",
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("release-manifest.sh: %v\n%s", err, stderr.String())
	}

	dev, err := os.ReadFile(bundlePath)
	if err != nil {
		t.Fatal(err)
	}
	want := bytes.ReplaceAll(dev, []byte(image), []byte("ghcr.io/mihnk/zoneroute@"+digest))
	if !bytes.Equal(bytes.TrimSpace(out), bytes.TrimSpace(want)) {
		t.Errorf("the release manifest differs from install.yaml by more than the image reference")
	}
}
