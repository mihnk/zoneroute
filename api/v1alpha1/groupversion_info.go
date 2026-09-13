// Package v1alpha1 contains the dns.mihnk.org/v1alpha1 API types.
//
// +kubebuilder:object:generate=true
// +groupName=dns.mihnk.org
package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var (
	// GroupVersion is the group and version served by this package.
	GroupVersion = schema.GroupVersion{Group: "dns.mihnk.org", Version: "v1alpha1"}

	// SchemeBuilder collects the functions that register this package's types.
	SchemeBuilder = runtime.NewSchemeBuilder(addKnownTypes)

	// AddToScheme registers this package's types with a scheme.
	AddToScheme = SchemeBuilder.AddToScheme
)

func addKnownTypes(s *runtime.Scheme) error {
	s.AddKnownTypes(GroupVersion, &ZoneRoute{}, &ZoneRouteList{})
	metav1.AddToGroupVersion(s, GroupVersion)
	return nil
}
