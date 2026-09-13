// Command zoneroute-controller runs the ZoneRoute controller.
package main

import (
	"flag"
	"fmt"
	"log/slog"
	"os"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/mihnk/zoneroute/api/v1alpha1"
	"github.com/mihnk/zoneroute/internal/controller"
)

// leaderElectionID names the Lease that serialises controller instances.
// It is stable across releases so a rolling upgrade hands over cleanly.
const leaderElectionID = "zoneroute.dns.mihnk.org"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "zoneroute-controller:", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		clusterDomain string
		leaderElect   bool
		probeAddr     string
		metricsAddr   string
	)
	flag.StringVar(&clusterDomain, "cluster-domain", "cluster.local", "Kubernetes cluster domain; ZoneRoutes inside it are rejected as reserved.")
	flag.BoolVar(&leaderElect, "leader-elect", true, "Enable leader election so only one instance reconciles.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "Address for the healthz and readyz endpoints.")
	flag.StringVar(&metricsAddr, "metrics-bind-address", "0", "Address for the metrics endpoint; 0 disables it.")
	flag.Parse()

	domain, err := controller.ClusterDomain(clusterDomain)
	if err != nil {
		return err
	}

	ctrl.SetLogger(logr.FromSlogHandler(slog.NewTextHandler(os.Stderr, nil)))

	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		return err
	}
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		return err
	}

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		LeaderElection:         leaderElect,
		LeaderElectionID:       leaderElectionID,
		HealthProbeBindAddress: probeAddr,
		Metrics:                metricsserver.Options{BindAddress: metricsAddr},
		Cache: cache.Options{
			// ConfigMaps are only ever read from kube-system; do not cache
			// them cluster-wide. ZoneRoutes are cluster-scoped and unaffected.
			ByObject: map[client.Object]cache.ByObject{
				&corev1.ConfigMap{}: {Namespaces: map[string]cache.Config{controller.CoreDNSNamespace: {}}},
			},
		},
	})
	if err != nil {
		return fmt.Errorf("creating manager: %w", err)
	}

	r := &controller.Reconciler{Client: mgr.GetClient(), ClusterDomain: domain}
	if err := r.SetupWithManager(mgr); err != nil {
		return fmt.Errorf("setting up controller: %w", err)
	}
	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		return err
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		return err
	}

	return mgr.Start(ctrl.SetupSignalHandler())
}
