package controller

import (
	"fmt"
	"sort"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/mihnk/zoneroute/api/v1alpha1"
	"github.com/mihnk/zoneroute/internal/conflict"
	"github.com/mihnk/zoneroute/internal/corefile"
)

// acceptedCondition maps a conflict decision, plus the inspection's
// unresolved imports, to the Accepted condition. Every multi-item message
// is sorted here so it never depends on another package's ordering.
func acceptedCondition(d conflict.Decision, unresolved []corefile.Import, clusterDomain string, generation int64) metav1.Condition {
	c := metav1.Condition{Type: v1alpha1.ConditionAccepted, ObservedGeneration: generation}

	switch {
	case len(d.Reserved) > 0:
		zones := sortedStrings(d.Reserved)
		c.Status = metav1.ConditionFalse
		c.Reason = v1alpha1.ReasonReservedZone
		c.Message = fmt.Sprintf("Zones inside the cluster domain %s are reserved: %s.", clusterDomain, strings.Join(zones, ", "))

	case len(d.Owned) > 0:
		parts := make([]string, 0, len(d.Owned))
		for _, o := range d.Owned {
			parts = append(parts, fmt.Sprintf("zone %s is already served by CoreDNS (%s)", o.Zone, o.Source))
		}
		sort.Strings(parts)
		c.Status = metav1.ConditionFalse
		c.Reason = v1alpha1.ReasonZoneOwnedByCoreDNS
		c.Message = capitalize(strings.Join(parts, "; ")) + "."

	case len(d.Conflicts) > 0:
		parts := make([]string, 0, len(d.Conflicts))
		for _, zc := range d.Conflicts {
			parts = append(parts, fmt.Sprintf("zone %s is claimed by older ZoneRoute %s", zc.Zone, zc.Winner))
		}
		sort.Strings(parts)
		c.Status = metav1.ConditionFalse
		c.Reason = v1alpha1.ReasonZoneConflict
		c.Message = capitalize(strings.Join(parts, "; ")) + "."

	case len(unresolved) > 0:
		parts := make([]string, 0, len(unresolved))
		for _, imp := range unresolved {
			parts = append(parts, fmt.Sprintf("%s:%d %s", imp.Source, imp.Line, imp.Pattern))
		}
		sort.Strings(parts)
		c.Status = metav1.ConditionTrue
		c.Reason = v1alpha1.ReasonAcceptedWithUnresolvedImports
		c.Message = "Accepted. Conflict detection could not inspect these imports: " + strings.Join(parts, "; ") + "."

	default:
		c.Status = metav1.ConditionTrue
		c.Reason = v1alpha1.ReasonAccepted
		c.Message = "All zones accepted."
	}
	return c
}

// publishedCondition maps acceptance and the publish outcome to the
// Published condition. Published=True asserts only that the route was
// accepted, the fragment was valid and written, and the Corefile wiring
// was detected; nothing about CoreDNS having loaded it.
func publishedCondition(accepted bool, state publishState, wired bool, generation int64) metav1.Condition {
	c := metav1.Condition{Type: v1alpha1.ConditionPublished, ObservedGeneration: generation, Status: metav1.ConditionFalse}

	switch {
	case !accepted:
		c.Reason = v1alpha1.ReasonNotAccepted
		c.Message = "Route is not accepted."

	case state.reason == v1alpha1.ReasonIntegrationConfigMissing:
		c.Reason = state.reason
		c.Message = fmt.Sprintf("ConfigMap %s/%s does not exist; create it to enable publishing.", CoreDNSNamespace, integrationConfigMap)

	case state.reason == v1alpha1.ReasonFragmentInvalid:
		c.Reason = state.reason
		c.Message = "Generated fragment failed validation: " + state.detail.Error()

	case state.reason == v1alpha1.ReasonWriteFailed:
		c.Reason = state.reason
		c.Message = "Failed to publish fragment: " + state.detail.Error()

	case !wired:
		c.Reason = v1alpha1.ReasonCoreDNSNotWired
		c.Message = fmt.Sprintf("Fragment written to %s/%s, but the Corefile has no top-level %q import.", CoreDNSNamespace, integrationConfigMap, "import "+corefile.WiringImport)

	default:
		c.Status = metav1.ConditionTrue
		c.Reason = v1alpha1.ReasonPublished
		c.Message = fmt.Sprintf("Fragment published to %s/%s (%s).", CoreDNSNamespace, integrationConfigMap, corefile.ManagedKey)
	}
	return c
}

func sortedStrings(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
