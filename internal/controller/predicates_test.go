package controller

import (
	"context"
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	"github.com/mihnk/zoneroute/api/v1alpha1"
)

func cm(namespace, name string, data map[string]string) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name, ResourceVersion: "1"},
		Data:       data,
	}
}

func withMeta(c *corev1.ConfigMap, rv string, annotations map[string]string) *corev1.ConfigMap {
	out := c.DeepCopy()
	out.ResourceVersion = rv
	out.Annotations = annotations
	return out
}

func withData(c *corev1.ConfigMap, data map[string]string) *corev1.ConfigMap {
	out := c.DeepCopy()
	out.Data = data
	return out
}

func update(oldObj, newObj client.Object) event.UpdateEvent {
	return event.UpdateEvent{ObjectOld: oldObj, ObjectNew: newObj}
}

func TestZoneRoutePredicate(t *testing.T) {
	p := predicate.GenerationChangedPredicate{}
	base := zoneRoute("r", t0, 1, []string{"x.test"}, "10.0.0.1")

	t.Run("create, spec change and delete pass", func(t *testing.T) {
		if !p.Create(event.CreateEvent{Object: base}) {
			t.Error("create should pass")
		}
		bumped := base.DeepCopy()
		bumped.Generation = 2
		if !p.Update(update(base, bumped)) {
			t.Error("generation change should pass")
		}
		if !p.Delete(event.DeleteEvent{Object: base}) {
			t.Error("delete should pass")
		}
	})

	t.Run("status-only update is ignored", func(t *testing.T) {
		statusOnly := base.DeepCopy()
		statusOnly.Status.ObservedGeneration = 1
		statusOnly.Status.Conditions = []metav1.Condition{{Type: v1alpha1.ConditionAccepted, Status: metav1.ConditionTrue}}
		if p.Update(update(base, statusOnly)) {
			t.Error("status-only update must not enqueue")
		}
	})
}

func TestCoreDNSConfigMapPredicate(t *testing.T) {
	p := coreDNSConfigMapPredicate()

	coredns := cm(CoreDNSNamespace, corefileConfigMap, map[string]string{corefileKey: corefileUnwired, "NodeHosts": "10.0.0.1 node"})
	custom := cm(CoreDNSNamespace, integrationConfigMap, map[string]string{
		"foo.server":       "a.test {\n}\n",
		"log.override":     "log\n",
		"notes":            "x",
		"zoneroute.server": "# managed\n",
	})

	t.Run("coredns create and delete", func(t *testing.T) {
		if !p.Create(event.CreateEvent{Object: coredns}) {
			t.Error("create should pass")
		}
		if !p.Delete(event.DeleteEvent{Object: coredns}) {
			t.Error("delete should pass")
		}
		if !p.Delete(event.DeleteEvent{Object: coredns, DeleteStateUnknown: true}) {
			t.Error("tombstone delete should pass")
		}
	})

	t.Run("coredns updates", func(t *testing.T) {
		tests := []struct {
			name string
			new  *corev1.ConfigMap
			want bool
		}{
			{"Corefile changed", withData(coredns, map[string]string{corefileKey: corefileWired, "NodeHosts": "10.0.0.1 node"}), true},
			{"Corefile key removed", withData(coredns, map[string]string{"NodeHosts": "10.0.0.1 node"}), true},
			{"metadata only", withMeta(coredns, "2", map[string]string{"a": "b"}), false},
			{"unrelated key only", withData(coredns, map[string]string{corefileKey: corefileUnwired, "NodeHosts": "10.0.0.2 node"}), false},
			{"binaryData only", func() *corev1.ConfigMap {
				c := coredns.DeepCopy()
				c.BinaryData = map[string][]byte{"blob": {1}}
				return c
			}(), false},
			{"identical", coredns.DeepCopy(), false},
		}
		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				if got := p.Update(update(coredns, tc.new)); got != tc.want {
					t.Errorf("Update = %v, want %v", got, tc.want)
				}
			})
		}
	})

	t.Run("coredns-custom create and delete", func(t *testing.T) {
		if !p.Create(event.CreateEvent{Object: custom}) {
			t.Error("create should pass")
		}
		if !p.Delete(event.DeleteEvent{Object: custom}) {
			t.Error("delete should pass")
		}
	})

	t.Run("coredns-custom updates", func(t *testing.T) {
		with := func(changes map[string]*string) *corev1.ConfigMap {
			data := make(map[string]string, len(custom.Data))
			for k, v := range custom.Data {
				data[k] = v
			}
			for k, v := range changes {
				if v == nil {
					delete(data, k)
				} else {
					data[k] = *v
				}
			}
			return withData(custom, data)
		}
		s := func(v string) *string { return &v }

		tests := []struct {
			name string
			new  *corev1.ConfigMap
			want bool
		}{
			{"server key added", with(map[string]*string{"bar.server": s("b.test {\n}\n")}), true},
			{"server key changed", with(map[string]*string{"foo.server": s("a.test {\n    forward . 1.1.1.1\n}\n")}), true},
			{"server key removed", with(map[string]*string{"foo.server": nil}), true},
			{"zoneroute.server changed", with(map[string]*string{"zoneroute.server": s("# rewritten\n")}), true},
			{"only override changed", with(map[string]*string{"log.override": s("log\nerrors\n")}), false},
			{"only unrelated key changed", with(map[string]*string{"notes": s("y")}), false},
			{"metadata only", withMeta(custom, "2", map[string]string{"a": "b"}), false},
			{"binaryData only", func() *corev1.ConfigMap {
				c := custom.DeepCopy()
				c.BinaryData = map[string][]byte{"blob.server": {1}}
				return c
			}(), false},
			{"suffix is case-sensitive", with(map[string]*string{"Foo.SERVER": s("x")}), false},
			{"identical", custom.DeepCopy(), false},
		}
		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				if got := p.Update(update(custom, tc.new)); got != tc.want {
					t.Errorf("Update = %v, want %v", got, tc.want)
				}
			})
		}
	})

	t.Run("nil and empty data are equivalent", func(t *testing.T) {
		nilData := cm(CoreDNSNamespace, integrationConfigMap, nil)
		emptyData := cm(CoreDNSNamespace, integrationConfigMap, map[string]string{})
		if p.Update(update(nilData, emptyData)) || p.Update(update(emptyData, nilData)) {
			t.Error("nil vs empty data must not enqueue")
		}
	})

	t.Run("other ConfigMaps are ignored", func(t *testing.T) {
		others := []*corev1.ConfigMap{
			cm("default", corefileConfigMap, map[string]string{corefileKey: corefileWired}),
			cm("default", integrationConfigMap, map[string]string{"foo.server": "x"}),
			cm(CoreDNSNamespace, "extension-apiserver-authentication", map[string]string{"client-ca-file": "x"}),
		}
		for _, o := range others {
			changed := withData(o, map[string]string{"anything": "changed", corefileKey: "new", "x.server": "new"})
			if p.Create(event.CreateEvent{Object: o}) || p.Update(update(o, changed)) || p.Delete(event.DeleteEvent{Object: o}) {
				t.Errorf("%s/%s must be ignored", o.Namespace, o.Name)
			}
		}
	})

	t.Run("generic events are ignored", func(t *testing.T) {
		if p.Generic(event.GenericEvent{Object: coredns}) {
			t.Error("generic event must not enqueue")
		}
	})

	t.Run("non-ConfigMap objects in an update are ignored without panicking", func(t *testing.T) {
		zr := zoneRoute(corefileConfigMap, t0, 1, []string{"x.test"}, "10.0.0.1")
		zr.Namespace = CoreDNSNamespace
		if p.Update(update(zr, zr)) {
			t.Error("non-ConfigMap must not enqueue")
		}
	})
}

func TestServerEntries(t *testing.T) {
	data := map[string]string{
		"foo.server":       "a",
		"zoneroute.server": "b",
		"log.override":     "c",
		"notes":            "d",
		"Bar.SERVER":       "e",
	}
	snapshot := map[string]string{
		"foo.server":       "a",
		"zoneroute.server": "b",
		"log.override":     "c",
		"notes":            "d",
		"Bar.SERVER":       "e",
	}

	got := serverEntries(data)
	want := map[string]string{"foo.server": "a", "zoneroute.server": "b"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("serverEntries = %v, want %v", got, want)
	}
	if !reflect.DeepEqual(data, snapshot) {
		t.Errorf("input mutated: %v", data)
	}
	got["foo.server"] = "mutated"
	if data["foo.server"] != "a" {
		t.Errorf("result aliases the input map")
	}
}

func TestAllSourcesMapToGlobalRequest(t *testing.T) {
	objs := []client.Object{
		zoneRoute("r", t0, 1, []string{"x.test"}, "10.0.0.1"),
		cm(CoreDNSNamespace, corefileConfigMap, nil),
		cm(CoreDNSNamespace, integrationConfigMap, nil),
	}
	for _, o := range objs {
		got := mapToGlobal(context.Background(), o)
		if len(got) != 1 || got[0] != globalRequest {
			t.Errorf("mapToGlobal(%s/%s) = %v, want [%v]", o.GetNamespace(), o.GetName(), got, globalRequest)
		}
	}
}
