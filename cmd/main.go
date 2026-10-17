// Package main is the entrypoint of the ml-experiment-operator.
//
// It initializes the runtime scheme (api/v1 + client-go), creates a
// controller-runtime Manager with Prometheus metrics, health/readiness
// probes and leader election, registers ExperimentReconciler and
// TrialReconciler, and starts the manager.
package main

import (
	"flag"
	"os"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	mlv1alpha1 "github.com/myorg/ml-experiment-operator/api/v1"
	"github.com/myorg/ml-experiment-operator/internal/controller"
	"github.com/myorg/ml-experiment-operator/internal/search"
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	// 1. Scheme initialization: core Kubernetes types (Pods, Jobs, PVCs, Events...)
	// and our custom api/v1 types (Experiment, Trial).
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(mlv1alpha1.AddToScheme(scheme))
}

func main() {
	// 6. Command-line flags.
	var (
		metricsAddr          string
		probeAddr            string
		enableLeaderElection bool
	)
	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080",
		"The address the metric endpoint binds to.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081",
		"The address the probe endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false,
		"Enable leader election for controller manager. "+
			"Ensure only one active controller manager when running multiple replicas.")

	opts := zap.Options{Development: true}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	// 2. Manager creation with Prometheus metrics server.
	// (controller-runtime v0.14 exposes the metrics endpoint via MetricsBindAddress;
	//  the default registry already serves Go/controller-runtime/ReconcilerMetrics,
	//  plus our custom trial_*/experiment_* collectors registered in pkg/mod.)
	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		MetricsBindAddress:     metricsAddr,
		Port:                   9443, // webhook server port (webhooks are added later)
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         enableLeaderElection,
		LeaderElectionID:       "ml-experiment-operator.example.com",
	})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	// 3. Register reconcilers.
	if err := (&controller.ExperimentReconciler{
		Client:         mgr.GetClient(),
		Scheme:         mgr.GetScheme(),
		Recorder:       mgr.GetEventRecorderFor("experiment-controller"),
		SearchRegistry: search.DefaultRegistry(),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "Experiment")
		os.Exit(1)
	}

	if err := (&controller.TrialReconciler{
		Client:   mgr.GetClient(),
		Scheme:   mgr.GetScheme(),
		Recorder: mgr.GetEventRecorderFor("trial-controller"),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "Trial")
		os.Exit(1)
	}

	// 4. Health and readiness probes (wired to kubelet /healthz and /readyz).
	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up ready check")
		os.Exit(1)
	}

	// 5. Start the manager (blocks until SIGTERM/SIGINT via signal handler).
	setupLog.Info("starting manager")
	startManager(setupLog, mgr)
}

// startManager runs mgr.Start; split out so it can be stubbed in tests.
func startManager(log logr.Logger, mgr ctrl.Manager) {
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		log.Error(err, "problem running manager")
		os.Exit(1)
	}
}
