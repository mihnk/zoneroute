package controller

import (
	"errors"
	"math/rand/v2"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/mihnk/zoneroute/api/v1alpha1"
	"github.com/mihnk/zoneroute/internal/conflict"
	"github.com/mihnk/zoneroute/internal/corefile"
)

// The FragmentInvalid path cannot be reached through Reconcile with valid
// API input (see internal/render), so the mapping is tested here.
func TestPublishedConditionMapping(t *testing.T) {
	tests := []struct {
		name       string
		accepted   bool
		state      publishState
		wired      bool
		wantStatus metav1.ConditionStatus
		wantReason string
		wantSubstr string
	}{
		{"not accepted wins over everything", false, publishState{reason: v1alpha1.ReasonWriteFailed, detail: errors.New("x")}, true, metav1.ConditionFalse, v1alpha1.ReasonNotAccepted, "not accepted"},
		{"integration config missing", true, publishState{reason: v1alpha1.ReasonIntegrationConfigMissing}, true, metav1.ConditionFalse, v1alpha1.ReasonIntegrationConfigMissing, "coredns-custom does not exist"},
		{"fragment invalid", true, publishState{reason: v1alpha1.ReasonFragmentInvalid, detail: errors.New("does not parse")}, true, metav1.ConditionFalse, v1alpha1.ReasonFragmentInvalid, "does not parse"},
		{"write failed", true, publishState{reason: v1alpha1.ReasonWriteFailed, detail: errors.New("boom")}, true, metav1.ConditionFalse, v1alpha1.ReasonWriteFailed, "boom"},
		{"written but not wired", true, publishState{}, false, metav1.ConditionFalse, v1alpha1.ReasonCoreDNSNotWired, "import custom/*.server"},
		{"published", true, publishState{}, true, metav1.ConditionTrue, v1alpha1.ReasonPublished, "zoneroute.server"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := publishedCondition(tc.accepted, tc.state, tc.wired, 4)
			if c.Type != v1alpha1.ConditionPublished || c.ObservedGeneration != 4 {
				t.Errorf("type/generation = %s/%d", c.Type, c.ObservedGeneration)
			}
			if c.Status != tc.wantStatus || c.Reason != tc.wantReason {
				t.Errorf("got %s/%s, want %s/%s", c.Status, c.Reason, tc.wantStatus, tc.wantReason)
			}
			if !strings.Contains(c.Message, tc.wantSubstr) {
				t.Errorf("message %q lacks %q", c.Message, tc.wantSubstr)
			}
		})
	}
}

func TestAcceptedConditionMessagesAreSorted(t *testing.T) {
	const domain = "cluster.local."

	t.Run("reserved", func(t *testing.T) {
		d := conflict.Decision{Name: "r", Reserved: []string{"z.svc.cluster.local.", "a.svc.cluster.local."}}
		c := acceptedCondition(d, nil, domain, 1)
		if c.Reason != v1alpha1.ReasonReservedZone || !strings.Contains(c.Message, "a.svc.cluster.local., z.svc.cluster.local.") {
			t.Errorf("%s: %q", c.Reason, c.Message)
		}
	})

	t.Run("owned", func(t *testing.T) {
		d := conflict.Decision{Name: "r", Owned: []conflict.OwnedByCoreDNS{
			{Zone: "b.test.", Port: 53, Source: "Corefile"},
			{Zone: "a.test.", Port: 53, Source: "foo.server"},
		}}
		c := acceptedCondition(d, nil, domain, 1)
		if c.Reason != v1alpha1.ReasonZoneOwnedByCoreDNS ||
			strings.Index(c.Message, "a.test.") > strings.Index(c.Message, "b.test.") ||
			!strings.Contains(c.Message, "foo.server") {
			t.Errorf("%s: %q", c.Reason, c.Message)
		}
	})

	t.Run("conflicts", func(t *testing.T) {
		d := conflict.Decision{Name: "r", Conflicts: []conflict.ZoneConflict{
			{Zone: "b.test.", Winner: "w1"},
			{Zone: "a.test.", Winner: "w2"},
		}}
		c := acceptedCondition(d, nil, domain, 1)
		if c.Reason != v1alpha1.ReasonZoneConflict || strings.Index(c.Message, "a.test.") > strings.Index(c.Message, "b.test.") {
			t.Errorf("%s: %q", c.Reason, c.Message)
		}
	})

	t.Run("unresolved imports", func(t *testing.T) {
		imports := []corefile.Import{
			{Source: "foo.server", Line: 1, Pattern: "/x.conf"},
			{Source: "Corefile", Line: 9, Pattern: "/b.conf"},
			{Source: "Corefile", Line: 2, Pattern: "/a.conf"},
		}
		want := "Corefile:2 /a.conf; Corefile:9 /b.conf; foo.server:1 /x.conf"
		for i := range 20 {
			shuffled := append([]corefile.Import(nil), imports...)
			rand.Shuffle(len(shuffled), func(a, b int) { shuffled[a], shuffled[b] = shuffled[b], shuffled[a] })
			c := acceptedCondition(conflict.Decision{Name: "r", Accepted: true}, shuffled, domain, 1)
			if c.Reason != v1alpha1.ReasonAcceptedWithUnresolvedImports || !strings.Contains(c.Message, want) {
				t.Fatalf("iteration %d: %s: %q", i, c.Reason, c.Message)
			}
		}
	})

	t.Run("accepted", func(t *testing.T) {
		c := acceptedCondition(conflict.Decision{Name: "r", Accepted: true}, nil, domain, 1)
		if c.Status != metav1.ConditionTrue || c.Reason != v1alpha1.ReasonAccepted {
			t.Errorf("%s/%s", c.Status, c.Reason)
		}
	})
}
