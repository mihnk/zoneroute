// Package controller is the only package that talks to Kubernetes. It
// adapts API objects into the inputs of the pure packages (corefile,
// conflict, render), publishes the rendered fragment, and writes status.
package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/mihnk/zoneroute/api/v1alpha1"
	"github.com/mihnk/zoneroute/internal/conflict"
	"github.com/mihnk/zoneroute/internal/corefile"
	"github.com/mihnk/zoneroute/internal/dnsname"
	"github.com/mihnk/zoneroute/internal/render"
)

// Where CoreDNS keeps its configuration. These are fixed by the v0.1
// installation contract.
const (
	CoreDNSNamespace     = "kube-system"
	corefileConfigMap    = "coredns"
	corefileKey          = "Corefile"
	integrationConfigMap = "coredns-custom"
)

// globalRequest is the single reconcile key. Every event source maps to it,
// so the workqueue collapses any burst of events into one full recompute.
var globalRequest = reconcile.Request{NamespacedName: types.NamespacedName{Name: "zoneroute"}}

// Reconciler recomputes the complete desired state on every run. It never
// reconciles a single ZoneRoute in isolation.
type Reconciler struct {
	client.Client
	// ClusterDomain in canonical form; see ClusterDomain.
	ClusterDomain string
}

// ClusterDomain validates the --cluster-domain flag and returns it in
// canonical form. It is the startup gate for reserved-zone protection: an
// empty or malformed value must stop the manager rather than weaken it.
func ClusterDomain(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", errors.New("cluster domain must not be empty")
	}
	c := dnsname.Canonical(s)
	if errs := validation.IsDNS1123Subdomain(strings.TrimSuffix(c, ".")); len(errs) > 0 {
		return "", fmt.Errorf("invalid cluster domain %q: %s", raw, strings.Join(errs, "; "))
	}
	return c, nil
}

// SetupWithManager registers the watches. Every source maps to the same
// global request. For() is deliberately not used: it would make the object
// name the reconcile key.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named("zoneroute").
		Watches(&v1alpha1.ZoneRoute{},
			handler.EnqueueRequestsFromMapFunc(mapToGlobal),
			builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Watches(&corev1.ConfigMap{},
			handler.EnqueueRequestsFromMapFunc(mapToGlobal),
			builder.WithPredicates(coreDNSConfigMapPredicate())).
		Complete(r)
}

func mapToGlobal(context.Context, client.Object) []reconcile.Request {
	return []reconcile.Request{globalRequest}
}

// Reconcile performs one full recompute. The request is ignored: there is
// only one.
func (r *Reconciler) Reconcile(ctx context.Context, _ reconcile.Request) (reconcile.Result, error) {
	var list v1alpha1.ZoneRouteList
	if err := r.List(ctx, &list); err != nil {
		return reconcile.Result{}, fmt.Errorf("listing ZoneRoutes: %w", err)
	}
	items := list.Items
	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })

	// Fail closed when CoreDNS configuration is not visible: without it,
	// conflict detection cannot be trusted and nothing may be published.
	var coredns corev1.ConfigMap
	if err := r.Get(ctx, types.NamespacedName{Namespace: CoreDNSNamespace, Name: corefileConfigMap}, &coredns); err != nil {
		return reconcile.Result{}, fmt.Errorf("reading ConfigMap %s/%s: %w", CoreDNSNamespace, corefileConfigMap, err)
	}
	corefileText, ok := coredns.Data[corefileKey]
	if !ok {
		return reconcile.Result{}, fmt.Errorf("ConfigMap %s/%s has no %q key", CoreDNSNamespace, corefileConfigMap, corefileKey)
	}

	var custom corev1.ConfigMap
	haveCustom := true
	if err := r.Get(ctx, types.NamespacedName{Namespace: CoreDNSNamespace, Name: integrationConfigMap}, &custom); err != nil {
		if !apierrors.IsNotFound(err) {
			return reconcile.Result{}, fmt.Errorf("reading ConfigMap %s/%s: %w", CoreDNSNamespace, integrationConfigMap, err)
		}
		haveCustom = false
	}
	var servers map[string]string
	if haveCustom {
		servers = custom.Data
	}

	inspected, err := corefile.Inspect(corefileText, servers)
	if err != nil {
		return reconcile.Result{}, fmt.Errorf("inspecting CoreDNS configuration: %w", err)
	}

	decisions, err := conflict.Resolve(toConflictRoutes(items), toExisting(inspected), r.ClusterDomain)
	if err != nil {
		return reconcile.Result{}, err
	}
	byName := make(map[string]conflict.Decision, len(decisions))
	accepted := make(map[string]bool, len(decisions))
	for _, d := range decisions {
		byName[d.Name] = d
		if d.Accepted {
			accepted[d.Name] = true
		}
	}

	fragment, renderErr := render.Render(toRenderRoutes(items, accepted))
	state := r.publish(ctx, haveCustom, &custom, fragment, renderErr)

	var errs []error
	if state.reason == v1alpha1.ReasonWriteFailed {
		errs = append(errs, state.detail)
	}
	for i := range items {
		if err := r.updateStatus(ctx, &items[i], byName[items[i].Name], inspected, state); err != nil {
			errs = append(errs, err)
		}
	}
	return reconcile.Result{}, errors.Join(errs...)
}

// publishState is what publishing achieved, before the per-route
// Published condition is derived from it. An empty reason means the
// fragment is in place.
type publishState struct {
	reason string // "", or one of the Published False reasons that apply to every accepted route
	detail error  // for FragmentInvalid and WriteFailed messages
}

// publish writes the fragment into data["zoneroute.server"] and nothing
// else, using a JSON merge patch so no other key is ever touched.
func (r *Reconciler) publish(ctx context.Context, haveCustom bool, cm *corev1.ConfigMap, fragment []byte, renderErr error) publishState {
	if !haveCustom {
		return publishState{reason: v1alpha1.ReasonIntegrationConfigMissing}
	}
	if renderErr != nil {
		return publishState{reason: v1alpha1.ReasonFragmentInvalid, detail: renderErr}
	}
	if current, ok := cm.Data[corefile.ManagedKey]; ok && bytes.Equal([]byte(current), fragment) {
		return publishState{}
	}

	body, err := json.Marshal(map[string]any{
		"data": map[string]string{corefile.ManagedKey: string(fragment)},
	})
	if err != nil {
		return publishState{reason: v1alpha1.ReasonWriteFailed, detail: err}
	}
	if err := r.Patch(ctx, cm, client.RawPatch(types.MergePatchType, body)); err != nil {
		return publishState{reason: v1alpha1.ReasonWriteFailed, detail: fmt.Errorf("patching ConfigMap %s/%s: %w", CoreDNSNamespace, integrationConfigMap, err)}
	}
	return publishState{}
}

// updateStatus writes the two conditions and observedGeneration, but only
// when something actually changed, so a repeated reconcile is free of API
// writes and LastTransitionTime is preserved.
func (r *Reconciler) updateStatus(ctx context.Context, obj *v1alpha1.ZoneRoute, d conflict.Decision, inspected corefile.Result, state publishState) error {
	updated := obj.DeepCopy()

	changed := meta.SetStatusCondition(&updated.Status.Conditions, acceptedCondition(d, inspected.UnresolvedImports, r.ClusterDomain, obj.Generation))
	changed = meta.SetStatusCondition(&updated.Status.Conditions, publishedCondition(d.Accepted, state, inspected.Wired, obj.Generation)) || changed
	if updated.Status.ObservedGeneration != obj.Generation {
		updated.Status.ObservedGeneration = obj.Generation
		changed = true
	}
	if !changed {
		return nil
	}
	if err := r.Status().Update(ctx, updated); err != nil {
		return fmt.Errorf("updating status of ZoneRoute %s: %w", obj.Name, err)
	}
	return nil
}
