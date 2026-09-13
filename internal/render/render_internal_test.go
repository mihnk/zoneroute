package render

import "testing"

// The parser check cannot be triggered through Render's public surface:
// every opening brace the renderer writes is closed, and CoreDNS's parser
// accepts any zone or address text as a key. So the guard is tested
// directly, with the one structural defect it exists to catch.
func TestValidate(t *testing.T) {
	t.Run("rejects a block that is never closed", func(t *testing.T) {
		if err := validate([]byte("example.com {\n    forward . 10.0.0.1\n")); err == nil {
			t.Fatal("validate: want error, got nil")
		}
	})

	t.Run("accepts rendered output", func(t *testing.T) {
		out, err := Render([]Route{{
			Name: "r", Zones: []string{"example.com"}, Upstreams: []Upstream{{Address: "10.0.0.1"}},
		}})
		if err != nil {
			t.Fatalf("Render: %v", err)
		}
		if err := validate(out); err != nil {
			t.Fatalf("validate: %v", err)
		}
	})

	t.Run("accepts the empty fragment", func(t *testing.T) {
		if err := validate([]byte(header)); err != nil {
			t.Fatalf("validate: %v", err)
		}
	})
}
