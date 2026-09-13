package controller

import (
	"reflect"
	"testing"

	"github.com/mihnk/zoneroute/internal/conflict"
	"github.com/mihnk/zoneroute/internal/corefile"
)

func TestToExistingIsSortedAndComplete(t *testing.T) {
	res := corefile.Result{Listeners: map[corefile.Listener]string{
		{Transport: "tls", Zone: "a.test.", Port: 853}:  "Corefile",
		{Transport: "dns", Zone: "b.test.", Port: 53}:   "foo.server",
		{Transport: "dns", Zone: "a.test.", Port: 53}:   "Corefile",
		{Transport: "dns", Zone: "a.test.", Port: 1053}: "Corefile",
	}}
	want := []conflict.Existing{
		{Transport: "dns", Zone: "a.test.", Port: 53, Source: "Corefile"},
		{Transport: "dns", Zone: "a.test.", Port: 1053, Source: "Corefile"},
		{Transport: "dns", Zone: "b.test.", Port: 53, Source: "foo.server"},
		{Transport: "tls", Zone: "a.test.", Port: 853, Source: "Corefile"},
	}
	for i := range 50 {
		if got := toExisting(res); !reflect.DeepEqual(got, want) {
			t.Fatalf("iteration %d: toExisting = %+v, want %+v", i, got, want)
		}
	}
}
