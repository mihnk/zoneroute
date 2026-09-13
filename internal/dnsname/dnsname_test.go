package dnsname

import "testing"

func TestCanonical(t *testing.T) {
	const want = "example.com."

	for _, in := range []string{
		"Example.COM",
		"example.com",
		"example.com.",
		"EXAMPLE.COM.",
		"eXaMpLe.CoM",
	} {
		if got := Canonical(in); got != want {
			t.Errorf("Canonical(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCanonicalIsIdempotent(t *testing.T) {
	once := Canonical("Corp.Internal")
	if twice := Canonical(once); twice != once {
		t.Errorf("Canonical(Canonical(x)) = %q, want %q", twice, once)
	}
}
