package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Condition types reported in ZoneRouteStatus.
const (
	// ConditionAccepted is True when the ZoneRoute passed validation and
	// conflict checks against everything the controller can see.
	ConditionAccepted = "Accepted"

	// ConditionPublished is True when the controller successfully published
	// this ZoneRoute into the CoreDNS integration point under the documented
	// installation contract. It does not mean CoreDNS loaded the fragment,
	// that the mount exists, that the upstream is reachable, or that DNS
	// resolution works.
	ConditionPublished = "Published"
)

// Reasons for the Accepted condition.
const (
	ReasonAccepted                      = "Accepted"
	ReasonAcceptedWithUnresolvedImports = "AcceptedWithUnresolvedImports"
	ReasonZoneConflict                  = "ZoneConflict"
	ReasonZoneOwnedByCoreDNS            = "ZoneOwnedByCoreDNS"
	ReasonReservedZone                  = "ReservedZone"
)

// Reasons for the Published condition.
const (
	ReasonPublished                = "Published"
	ReasonNotAccepted              = "NotAccepted"
	ReasonIntegrationConfigMissing = "IntegrationConfigMissing"
	ReasonCoreDNSNotWired          = "CoreDNSNotWired"
	ReasonFragmentInvalid          = "FragmentInvalid"
	ReasonWriteFailed              = "WriteFailed"
)

// DefaultPort is the upstream port used when Upstream.Port is omitted.
// The API server applies it as a schema default, so objects read from the
// API always carry an explicit port.
const DefaultPort int32 = 53

// ZoneRouteSpec describes which DNS zones are forwarded to which resolvers.
type ZoneRouteSpec struct {
	// Zones lists the DNS zones routed to Upstreams. Order has no meaning.
	// Names are compared case-insensitively and with the trailing dot
	// ignored, so "Example.COM" and "example.com." are the same zone and
	// may not both appear.
	//
	// +listType=atomic
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=64
	// +kubebuilder:validation:items:MinLength=1
	// +kubebuilder:validation:items:MaxLength=254
	// +kubebuilder:validation:items:Pattern=`^(?i)([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)*[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.?$`
	// +kubebuilder:validation:XValidation:rule="self.all(i, self.filter(j, (j.lowerAscii().endsWith('.') ? j.lowerAscii().substring(0, size(j) - 1) : j.lowerAscii()) == (i.lowerAscii().endsWith('.') ? i.lowerAscii().substring(0, size(i) - 1) : i.lowerAscii())).size() == 1)",message="zones must be unique ignoring case and trailing dot"
	// +kubebuilder:validation:XValidation:rule="self.all(z, !((z.lowerAscii().endsWith('.') ? z.lowerAscii().substring(0, size(z) - 1) : z.lowerAscii()) in ['localhost', 'in-addr.arpa', 'ip6.arpa'] || z.lowerAscii().endsWith('.localhost') || z.lowerAscii().endsWith('.localhost.') || z.lowerAscii().endsWith('.in-addr.arpa') || z.lowerAscii().endsWith('.in-addr.arpa.') || z.lowerAscii().endsWith('.ip6.arpa') || z.lowerAscii().endsWith('.ip6.arpa.')))",message="zone is reserved: localhost, in-addr.arpa and ip6.arpa may not be routed"
	Zones []string `json:"zones"`

	// Upstreams lists the resolvers that answer for Zones. ORDER IS
	// SIGNIFICANT: the first upstream is tried before the second. Two
	// entries with the same address and port are not allowed.
	//
	// +listType=atomic
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=8
	// +kubebuilder:validation:XValidation:rule="self.all(i, self.filter(j, j.address == i.address && j.port == i.port).size() == 1)",message="upstreams must be unique by address and port"
	Upstreams []Upstream `json:"upstreams"`
}

// Upstream is one DNS resolver.
type Upstream struct {
	// Address is a canonical IPv4 or IPv6 address. Hostnames are not
	// accepted, and non-canonical forms such as "0:0:0:0:0:0:0:1" are
	// rejected so that addresses compare as plain strings.
	//
	// ip.isCanonical is a plain function taking a string (see
	// k8s.io/apiserver/pkg/cel/library/ip.go); the member form
	// ip(x).isCanonical() shown in some documentation does not compile on
	// Kubernetes 1.31.
	//
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=45
	// +kubebuilder:validation:XValidation:rule="isIP(self) && ip.isCanonical(self)",message="address must be a canonical IPv4 or IPv6 address"
	Address string `json:"address"`

	// Port is the resolver's UDP/TCP port. Defaults to 53.
	//
	// +optional
	// +kubebuilder:default=53
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	Port int32 `json:"port,omitempty"`
}

// ZoneRouteStatus reports what the controller last observed and did.
type ZoneRouteStatus struct {
	// ObservedGeneration is the metadata.generation the conditions refer to.
	//
	// +optional
	// +kubebuilder:validation:Minimum=0
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions holds the Accepted and Published conditions.
	//
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// ZoneRoute forwards DNS queries for a set of zones to an ordered list of
// upstream resolvers, by publishing a CoreDNS configuration fragment.
//
// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="ACCEPTED",type=string,JSONPath=`.status.conditions[?(@.type=="Accepted")].status`
// +kubebuilder:printcolumn:name="PUBLISHED",type=string,JSONPath=`.status.conditions[?(@.type=="Published")].status`
// +kubebuilder:printcolumn:name="AGE",type=date,JSONPath=`.metadata.creationTimestamp`
type ZoneRoute struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ZoneRouteSpec   `json:"spec"`
	Status ZoneRouteStatus `json:"status,omitempty"`
}

// ZoneRouteList is a list of ZoneRoute.
//
// +kubebuilder:object:root=true
type ZoneRouteList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ZoneRoute `json:"items"`
}
