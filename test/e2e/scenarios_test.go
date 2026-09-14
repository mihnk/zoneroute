//go:build e2e

package e2e

import (
	"crypto/sha256"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/mihnk/zoneroute/api/v1alpha1"
)

// Scenarios share one controller and one CoreDNS, so they run in this order
// and each one removes what it created. Zone names are reused on purpose:
// a scenario starting with a stale fragment is a bug in the previous one.

const (
	zone      = "zoneroute.test"
	helloName = "hello.zoneroute.test."
)

// The production RBAC must allow exactly what the controller does. The suite
// as a whole proves sufficiency; this pins the boundary.
func TestRBACBoundary(t *testing.T) {
	tests := []struct {
		verb, group, resource, subresource, namespace, name string
		want                                                bool
	}{
		{"list", "dns.mihnk.org", "zoneroutes", "", "", "", true},
		{"watch", "dns.mihnk.org", "zoneroutes", "", "", "", true},
		{"update", "dns.mihnk.org", "zoneroutes", "status", "", "happy", true},
		{"get", "dns.mihnk.org", "zoneroutes", "", "", "happy", false},
		{"update", "dns.mihnk.org", "zoneroutes", "", "", "happy", false},
		{"delete", "dns.mihnk.org", "zoneroutes", "", "", "happy", false},
		{"list", "", "configmaps", "", coreDNSNamespace, "", true},
		{"watch", "", "configmaps", "", coreDNSNamespace, "", true},
		{"patch", "", "configmaps", "", coreDNSNamespace, "coredns-custom", true},
		{"patch", "", "configmaps", "", coreDNSNamespace, "coredns", false},
		{"update", "", "configmaps", "", coreDNSNamespace, "coredns-custom", false},
		{"create", "", "configmaps", "", coreDNSNamespace, "", false},
		{"delete", "", "configmaps", "", coreDNSNamespace, "coredns-custom", false},
		{"list", "", "configmaps", "", "default", "", false},
		{"get", "", "secrets", "", coreDNSNamespace, "", false},
		{"list", "", "pods", "", coreDNSNamespace, "", false},
		{"patch", "apps", "deployments", "", coreDNSNamespace, "coredns", false},
		{"create", "", "events", "", controllerNamespace, "", false},
		{"create", "coordination.k8s.io", "leases", "", controllerNamespace, "", true},
		{"update", "coordination.k8s.io", "leases", "", controllerNamespace, "zoneroute.dns.mihnk.org", true},
		{"update", "coordination.k8s.io", "leases", "", controllerNamespace, "other", false},
		{"update", "coordination.k8s.io", "leases", "", coreDNSNamespace, "zoneroute.dns.mihnk.org", false},
	}
	for _, tc := range tests {
		if got := canI(t, tc.verb, tc.group, tc.resource, tc.subresource, tc.namespace, tc.name); got != tc.want {
			t.Errorf("%s %s/%s%s %s/%s: allowed=%v, want %v", tc.verb, tc.group, tc.resource, sub(tc.subresource), tc.namespace, tc.name, got, tc.want)
		}
	}
}

func sub(s string) string {
	if s == "" {
		return ""
	}
	return "/" + s
}

// Before any route exists the controller has published the empty fragment
// and the test zone resolves through the default path, not to our upstream.
func TestInitialState(t *testing.T) {
	waitFragment(t, expectFragment(t))
	if got := dig(t, helloName, false, ""); got == answerA {
		t.Fatalf("%s already resolves to %s before any ZoneRoute", helloName, answerA)
	}
}

// 1 + 2: the complete path, and ordinary cluster DNS untouched.
func TestHappyPath(t *testing.T) {
	zr := createRoute(t, "happy", []string{zone}, upstream{upstreamA, 53})
	waitCondition(t, "happy", v1alpha1.ConditionAccepted, metav1.ConditionTrue, v1alpha1.ReasonAccepted)
	waitCondition(t, "happy", v1alpha1.ConditionPublished, metav1.ConditionTrue, v1alpha1.ReasonPublished)

	want := expectFragment(t, zr)
	if !strings.Contains(want, "forward . "+upstreamA+" {") {
		t.Fatalf("expected fragment does not forward to %s:\n%s", upstreamA, want)
	}
	waitFragment(t, want)

	waitDNS(t, helloName, answerA)
	waitDNSOver(t, helloName, answerA, true)
	if got := dig(t, "nothing."+zone+".", false, ""); got != "" {
		t.Errorf("name unknown to the upstream answered %q", got)
	}
	if got := dig(t, "kubernetes.default.svc.cluster.local.", false, ""); got != kubernetesIP {
		t.Errorf("cluster DNS: got %q, want %s", got, kubernetesIP)
	}
}

// 3: two routes for the same zone; the older one wins deterministically.
// creationTimestamp has one-second resolution, so the names are chosen so
// that the tiebreak (name order) picks the same winner.
func TestZoneConflict(t *testing.T) {
	a := createRoute(t, "conflict-a", []string{zone}, upstream{upstreamA, 53})
	waitCondition(t, "conflict-a", v1alpha1.ConditionAccepted, metav1.ConditionTrue, v1alpha1.ReasonAccepted)

	createRoute(t, "conflict-b", []string{zone}, upstream{upstreamB, 53})
	c := waitCondition(t, "conflict-b", v1alpha1.ConditionAccepted, metav1.ConditionFalse, v1alpha1.ReasonZoneConflict)
	if !strings.Contains(c.Message, "conflict-a") {
		t.Errorf("conflict message does not name the winner: %q", c.Message)
	}
	waitCondition(t, "conflict-b", v1alpha1.ConditionPublished, metav1.ConditionFalse, v1alpha1.ReasonNotAccepted)

	waitFragment(t, expectFragment(t, a))
	waitCondition(t, "conflict-a", v1alpha1.ConditionAccepted, metav1.ConditionTrue, v1alpha1.ReasonAccepted)
	// upstream-b does not know hello.zoneroute.test, so this answer can only
	// come from the winner.
	waitDNS(t, helloName, answerA)
}

// 4: case and trailing dot do not make a different zone.
func TestCanonicalEquivalence(t *testing.T) {
	a := createRoute(t, "canon-a", []string{"ZONEROUTE.TEST"}, upstream{upstreamA, 53})
	waitCondition(t, "canon-a", v1alpha1.ConditionAccepted, metav1.ConditionTrue, v1alpha1.ReasonAccepted)
	createRoute(t, "canon-b", []string{"zoneroute.test."}, upstream{upstreamB, 53})
	waitCondition(t, "canon-b", v1alpha1.ConditionAccepted, metav1.ConditionFalse, v1alpha1.ReasonZoneConflict)

	want := expectFragment(t, a)
	if !strings.Contains(want, "\n"+zone+" {\n") {
		t.Fatalf("fragment does not carry the canonical zone:\n%s", want)
	}
	waitFragment(t, want)
}

// 5: a parent and a child zone coexist and CoreDNS picks the longest match.
func TestParentChildCoexist(t *testing.T) {
	parent := createRoute(t, "parent", []string{zone}, upstream{upstreamA, 53})
	child := createRoute(t, "child", []string{"child." + zone}, upstream{upstreamB, 53})
	waitCondition(t, "parent", v1alpha1.ConditionAccepted, metav1.ConditionTrue, v1alpha1.ReasonAccepted)
	waitCondition(t, "child", v1alpha1.ConditionAccepted, metav1.ConditionTrue, v1alpha1.ReasonAccepted)
	waitCondition(t, "parent", v1alpha1.ConditionPublished, metav1.ConditionTrue, v1alpha1.ReasonPublished)
	waitCondition(t, "child", v1alpha1.ConditionPublished, metav1.ConditionTrue, v1alpha1.ReasonPublished)
	waitFragment(t, expectFragment(t, parent, child))

	waitDNS(t, "hello.child."+zone+".", answerB)
	waitDNS(t, helloName, answerA)
}

// 6: a zone already served through another *.server file is CoreDNS's.
func TestZoneOwnedByUserServer(t *testing.T) {
	setCustomKey(t, "user.server", "owned.test {\n    hosts {\n        192.0.2.77 hello.owned.test\n    }\n}\n")
	waitDNS(t, "hello.owned.test.", "192.0.2.77") // CoreDNS loaded the user's file

	createRoute(t, "owned", []string{"owned.test"}, upstream{upstreamA, 53})
	c := waitCondition(t, "owned", v1alpha1.ConditionAccepted, metav1.ConditionFalse, v1alpha1.ReasonZoneOwnedByCoreDNS)
	if !strings.Contains(c.Message, "user.server") {
		t.Errorf("message does not name the owning file: %q", c.Message)
	}
	waitCondition(t, "owned", v1alpha1.ConditionPublished, metav1.ConditionFalse, v1alpha1.ReasonNotAccepted)
	waitFragment(t, expectFragment(t))
}

// 6b: ownership discovered directly from the main Corefile.
func TestZoneOwnedByCorefile(t *testing.T) {
	editCorefile(t, func(s string) string {
		return s + "owned2.test {\n    hosts {\n        192.0.2.78 hello.owned2.test\n    }\n}\n"
	})
	waitDNS(t, "hello.owned2.test.", "192.0.2.78")

	createRoute(t, "owned2", []string{"owned2.test"}, upstream{upstreamA, 53})
	c := waitCondition(t, "owned2", v1alpha1.ConditionAccepted, metav1.ConditionFalse, v1alpha1.ReasonZoneOwnedByCoreDNS)
	if !strings.Contains(c.Message, "Corefile") {
		t.Errorf("message does not name the Corefile: %q", c.Message)
	}
	waitFragment(t, expectFragment(t))
}

// 7: without coredns-custom nothing is published and nothing is created.
func TestIntegrationConfigMissing(t *testing.T) {
	zr := createRoute(t, "missing", []string{zone}, upstream{upstreamA, 53})
	waitCondition(t, "missing", v1alpha1.ConditionPublished, metav1.ConditionTrue, v1alpha1.ReasonPublished)

	cm, _ := getConfigMap(coreDNSNamespace, "coredns-custom")
	ensure := func() {
		if _, ok := getConfigMap(coreDNSNamespace, "coredns-custom"); !ok {
			empty := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: coreDNSNamespace, Name: "coredns-custom"}}
			if err := kube.Create(ctx, empty); err != nil {
				t.Fatalf("recreating coredns-custom: %v", err)
			}
		}
	}
	t.Cleanup(ensure)
	if err := kube.Delete(ctx, cm); err != nil {
		t.Fatalf("deleting coredns-custom: %v", err)
	}

	waitCondition(t, "missing", v1alpha1.ConditionPublished, metav1.ConditionFalse, v1alpha1.ReasonIntegrationConfigMissing)
	waitCondition(t, "missing", v1alpha1.ConditionAccepted, metav1.ConditionTrue, v1alpha1.ReasonAccepted)
	consistently(t, settleWindow, "controller does not create coredns-custom", func() (bool, string) {
		_, ok := getConfigMap(coreDNSNamespace, "coredns-custom")
		return !ok, "coredns-custom exists"
	})

	ensure()
	waitCondition(t, "missing", v1alpha1.ConditionPublished, metav1.ConditionTrue, v1alpha1.ReasonPublished)
	waitFragment(t, expectFragment(t, zr))
}

// 8: the fragment is still written when the Corefile lacks the import, and
// Published says so.
func TestCoreDNSNotWired(t *testing.T) {
	zr := createRoute(t, "unwired", []string{zone}, upstream{upstreamA, 53})
	waitCondition(t, "unwired", v1alpha1.ConditionPublished, metav1.ConditionTrue, v1alpha1.ReasonPublished)

	previous := editCorefile(t, withoutImport)
	waitCondition(t, "unwired", v1alpha1.ConditionPublished, metav1.ConditionFalse, v1alpha1.ReasonCoreDNSNotWired)
	waitCondition(t, "unwired", v1alpha1.ConditionAccepted, metav1.ConditionTrue, v1alpha1.ReasonAccepted)
	waitFragment(t, expectFragment(t, zr))

	setCorefile(t, previous)
	waitCondition(t, "unwired", v1alpha1.ConditionPublished, metav1.ConditionTrue, v1alpha1.ReasonPublished)
}

// 9: an external edit of zoneroute.server is repaired by the ConfigMap
// watch alone; no ZoneRoute is touched.
func TestExternalEditIsRepaired(t *testing.T) {
	zr := createRoute(t, "repair", []string{zone}, upstream{upstreamA, 53})
	want := expectFragment(t, zr)
	waitFragment(t, want)

	garbage := "# not what the controller wrote\n"
	patchConfigMapData(t, coreDNSNamespace, "coredns-custom", map[string]*string{fragmentKey: &garbage})
	waitFragment(t, want)
	if got := getRoute(t, "repair").Generation; got != zr.Generation {
		t.Errorf("ZoneRoute generation changed to %d", got)
	}
}

// 10: a *.server file added later takes the zone away, and gives it back.
func TestLateUserServerTakesOver(t *testing.T) {
	zr := createRoute(t, "takeover", []string{zone}, upstream{upstreamA, 53})
	waitCondition(t, "takeover", v1alpha1.ConditionPublished, metav1.ConditionTrue, v1alpha1.ReasonPublished)
	waitDNS(t, helloName, answerA)

	setCustomKey(t, "user.server", zone+" {\n    hosts {\n        192.0.2.99 hello."+zone+"\n    }\n}\n")
	waitCondition(t, "takeover", v1alpha1.ConditionAccepted, metav1.ConditionFalse, v1alpha1.ReasonZoneOwnedByCoreDNS)
	waitFragment(t, expectFragment(t))
	waitDNS(t, helloName, "192.0.2.99")

	deleteCustomKey(t, "user.server")
	waitCondition(t, "takeover", v1alpha1.ConditionAccepted, metav1.ConditionTrue, v1alpha1.ReasonAccepted)
	waitFragment(t, expectFragment(t, zr))
	waitDNS(t, helloName, answerA)
}

// 11: deleting a route removes its block and its forwarding.
func TestDeleteRemovesForwarding(t *testing.T) {
	createRoute(t, "gone", []string{zone}, upstream{upstreamA, 53})
	waitDNS(t, helloName, answerA)

	deleteRoute(t, "gone")
	waitFragment(t, expectFragment(t))
	waitDNSNot(t, helloName, answerA)
}

// 12 + 13: a restart converges from the API objects without rewriting the
// fragment, and a benign external change does not make the controller write.
func TestRestartAndIdempotency(t *testing.T) {
	zr := createRoute(t, "stable", []string{zone}, upstream{upstreamA, 53})
	waitCondition(t, "stable", v1alpha1.ConditionPublished, metav1.ConditionTrue, v1alpha1.ReasonPublished)
	want := expectFragment(t, zr)
	waitFragment(t, want)
	_, rvBefore, _ := fragment()

	restartController(t)
	// The new pod is Ready before it holds the Lease. A route that is
	// rejected without touching the fragment proves the new instance has
	// reconciled, so the stability window below is not vacuous.
	createRoute(t, "probe", []string{"svc.cluster.local"}, upstream{upstreamA, 53})
	waitCondition(t, "probe", v1alpha1.ConditionAccepted, metav1.ConditionFalse, v1alpha1.ReasonReservedZone)
	waitCondition(t, "stable", v1alpha1.ConditionAccepted, metav1.ConditionTrue, v1alpha1.ReasonAccepted)
	waitCondition(t, "stable", v1alpha1.ConditionPublished, metav1.ConditionTrue, v1alpha1.ReasonPublished)
	consistently(t, settleWindow, "restart does not rewrite coredns-custom", func() (bool, string) {
		got, rv, _ := fragment()
		return got == want && rv == rvBefore, "resourceVersion " + rv + " (was " + rvBefore + ")\n" + got
	})

	// A benign external change necessarily bumps the resourceVersion once;
	// the controller must then leave it alone.
	cm := setCustomKey(t, "note.server", "note.test {\n    hosts {\n        192.0.2.5 hello.note.test\n    }\n}\n")
	rvAfterEdit := cm.ResourceVersion
	consistently(t, settleWindow, "benign *.server change does not make the controller write", func() (bool, string) {
		got, rv, _ := fragment()
		return got == want && rv == rvAfterEdit, "resourceVersion " + rv + " (external edit left " + rvAfterEdit + ")\n" + got
	})
}

// 14: the cluster domain is off limits and cluster DNS keeps working.
func TestReservedZone(t *testing.T) {
	createRoute(t, "reserved", []string{"svc.cluster.local"}, upstream{upstreamA, 53})
	waitCondition(t, "reserved", v1alpha1.ConditionAccepted, metav1.ConditionFalse, v1alpha1.ReasonReservedZone)
	waitCondition(t, "reserved", v1alpha1.ConditionPublished, metav1.ConditionFalse, v1alpha1.ReasonNotAccepted)
	waitFragment(t, expectFragment(t))
	if got := dig(t, "kubernetes.default.svc.cluster.local.", false, ""); got != kubernetesIP {
		t.Errorf("cluster DNS: got %q, want %s", got, kubernetesIP)
	}
}

// 15: an import the controller cannot read is reported, not fatal.
func TestUnresolvedImport(t *testing.T) {
	editCorefile(t, func(s string) string { return s + "import /etc/coredns/extra/*.conf\n" })

	createRoute(t, "imports", []string{zone}, upstream{upstreamA, 53})
	c := waitCondition(t, "imports", v1alpha1.ConditionAccepted, metav1.ConditionTrue, v1alpha1.ReasonAcceptedWithUnresolvedImports)
	if !strings.Contains(c.Message, "/etc/coredns/extra/*.conf") {
		t.Errorf("message does not name the import: %q", c.Message)
	}
	waitCondition(t, "imports", v1alpha1.ConditionPublished, metav1.ConditionTrue, v1alpha1.ReasonPublished)

	setCorefile(t, wiredCorefile)
	waitCondition(t, "imports", v1alpha1.ConditionAccepted, metav1.ConditionTrue, v1alpha1.ReasonAccepted)
}

// 16: a non-default upstream port reaches a listener that only exists there.
func TestNonDefaultPort(t *testing.T) {
	zr := createRoute(t, "port", []string{"port.test"}, upstream{upstreamA, 5353})
	waitCondition(t, "port", v1alpha1.ConditionPublished, metav1.ConditionTrue, v1alpha1.ReasonPublished)
	want := expectFragment(t, zr)
	if !strings.Contains(want, "forward . "+upstreamA+":5353 {") {
		t.Fatalf("fragment does not carry the port:\n%s", want)
	}
	waitFragment(t, want)
	waitDNS(t, "hello.port.test.", answerPort)
}

// The suite runs CoreDNS with one replica for speed, but the kubeadm default
// — and therefore the layout this project validates — is two, and each
// replica reloads on its own schedule. A fragment that reaches only one of
// them is the regression this scenario exists to catch: it was seen for real
// during #8, where UDP hit a reloaded replica and TCP hit one that had not
// caught up.
//
// The scenario restores everything it changes and verifies the restoration
// itself, so its correctness does not depend on running before any other
// test.
func TestMultiReplicaConvergence(t *testing.T) {
	const want = 2
	before := coreDNSReplicas(t)

	// Cleanups run last-in-first-out, so these are registered in reverse of
	// the order they must execute:
	//
	//   1. the route is deleted      (registered last, by createRoute)
	//   2. the replica count is restored
	//   3. the restoration is verified
	//
	// Registering the verification first is what makes it run after the
	// restore, and registering the restore before createRoute is what keeps
	// the route from outliving the replicas it was tested against.
	t.Cleanup(func() {
		ips := waitCoreDNSReplicas(t, before)
		if got := coreDNSReplicas(t); got != before {
			t.Errorf("CoreDNS replicas = %d after cleanup, want the original %d", got, before)
		}
		if len(ips) != int(before) {
			t.Errorf("%d usable CoreDNS pods after cleanup, want %d", len(ips), before)
		}
	})
	t.Cleanup(func() {
		if coreDNSReplicas(t) != before {
			setCoreDNSReplicas(t, before)
		}
	})

	ips := setCoreDNSReplicas(t, want)
	if len(ips) != want {
		t.Fatalf("got %d usable CoreDNS pod IPs (%v), want %d", len(ips), ips, want)
	}
	if ips[0] == ips[1] {
		t.Fatalf("both CoreDNS pods report the same IP: %v", ips)
	}
	t.Logf("CoreDNS replicas under test: %v", ips)

	zr := createRoute(t, "multireplica", []string{zone}, upstream{upstreamA, 53})
	waitCondition(t, "multireplica", v1alpha1.ConditionAccepted, metav1.ConditionTrue, v1alpha1.ReasonAccepted)
	waitCondition(t, "multireplica", v1alpha1.ConditionPublished, metav1.ConditionTrue, v1alpha1.ReasonPublished)

	// The fragment must be right before DNS is worth asking about.
	waitFragment(t, expectFragment(t, zr))

	// Every replica, over both transports. waitDNSOver queries each pod
	// directly as well as the Service, so an answer from one pod is never
	// enough to satisfy it.
	waitDNSOver(t, helloName, answerA, false)
	waitDNSOver(t, helloName, answerA, true)

	// State the per-replica result explicitly, so a future change to the
	// helpers cannot quietly reduce this to a Service-only assertion.
	for _, tcp := range []bool{false, true} {
		proto := "udp"
		if tcp {
			proto = "tcp"
		}
		answers := resolveEverywhere(t, helloName, tcp)
		for _, ip := range ips {
			got, asked := answers[ip]
			if !asked {
				t.Errorf("%s: replica %s was not queried (answers: %v)", proto, ip, answers)
				continue
			}
			if got != answerA {
				t.Errorf("%s: replica %s answered %q, want %s (answers: %v)", proto, ip, got, answerA, answers)
			}
		}
		t.Logf("%s answers per replica: %v", proto, answers)
	}
}

// The suite leaves CoreDNS as it found it after wiring.
func TestSuiteLeavesCoreDNSUntouched(t *testing.T) {
	if got, want := sha256.Sum256([]byte(corefile(t))), sha256.Sum256([]byte(wiredCorefile)); got != want {
		t.Errorf("Corefile changed during the suite:\n%s", corefile(t))
	}
	cm, ok := getConfigMap(coreDNSNamespace, "coredns-custom")
	if !ok {
		t.Fatal("coredns-custom is gone")
	}
	for k := range cm.Data {
		if k != fragmentKey {
			t.Errorf("leftover key %q in coredns-custom", k)
		}
	}
	waitFragment(t, expectFragment(t))
}
