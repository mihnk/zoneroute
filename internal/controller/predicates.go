package controller

import (
	"maps"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
)

// coreDNSConfigMapPredicate passes exactly the ConfigMap events that can
// change ZoneRoute's desired state: the two CoreDNS ConfigMaps in
// kube-system, and for updates only when the data ZoneRoute reads changed.
// Everything it passes is mapped to the single global reconcile request.
func coreDNSConfigMapPredicate() predicate.Funcs {
	return predicate.Funcs{
		CreateFunc:  func(e event.CreateEvent) bool { return isWatchedConfigMap(e.Object) },
		DeleteFunc:  func(e event.DeleteEvent) bool { return isWatchedConfigMap(e.Object) },
		UpdateFunc:  configMapDataChanged,
		GenericFunc: func(event.GenericEvent) bool { return false },
	}
}

// isWatchedConfigMap reports whether obj is kube-system/coredns or
// kube-system/coredns-custom. The cache is already scoped to kube-system;
// the namespace check makes the predicate correct on its own.
func isWatchedConfigMap(obj client.Object) bool {
	if obj == nil || obj.GetNamespace() != CoreDNSNamespace {
		return false
	}
	name := obj.GetName()
	return name == corefileConfigMap || name == integrationConfigMap
}

// configMapDataChanged decides whether an update to a watched ConfigMap
// touched the data ZoneRoute depends on. Metadata-only updates, unrelated
// keys and binaryData never count.
func configMapDataChanged(e event.UpdateEvent) bool {
	if !isWatchedConfigMap(e.ObjectNew) {
		return false
	}
	oldCM, okOld := e.ObjectOld.(*corev1.ConfigMap)
	newCM, okNew := e.ObjectNew.(*corev1.ConfigMap)
	if !okOld || !okNew {
		return false
	}

	switch newCM.Name {
	case corefileConfigMap:
		return oldCM.Data[corefileKey] != newCM.Data[corefileKey]
	case integrationConfigMap:
		// zoneroute.server is included on purpose: the controller's own
		// patch triggers one follow-up reconcile that finds identical
		// content and writes nothing, and a manual edit of the key gets
		// repaired the same way.
		return !maps.Equal(serverEntries(oldCM.Data), serverEntries(newCM.Data))
	}
	return false
}

// serverEntries copies the "*.server" entries of a ConfigMap's data into a
// fresh map for comparison. The suffix match is exact and case-sensitive,
// mirroring what internal/corefile parses. The input is never modified.
func serverEntries(data map[string]string) map[string]string {
	out := make(map[string]string)
	for k, v := range data {
		if strings.HasSuffix(k, ".server") {
			out[k] = v
		}
	}
	return out
}
