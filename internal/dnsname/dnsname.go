// Package dnsname normalizes DNS names into one canonical form so that
// names from different sources can be compared as plain strings.
package dnsname

import "strings"

// Canonical returns name in lowercase with exactly one trailing dot, which
// is the form CoreDNS uses internally for zone names.
//
// Canonical is a normalizer, not a validator: it expects a syntactically
// valid DNS name and makes no promises for anything else.
func Canonical(name string) string {
	return strings.ToLower(strings.TrimSuffix(name, ".")) + "."
}
