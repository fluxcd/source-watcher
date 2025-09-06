/*
Copyright 2025 The Flux authors

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"os"
	"time"

	"github.com/fluxcd/pkg/runtime/probes"
	flag "github.com/spf13/pflag"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	_ "k8s.io/client-go/plugin/pkg/client/auth"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"
	ctrlcfg "sigs.k8s.io/controller-runtime/pkg/config"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	artcfg "github.com/fluxcd/pkg/artifact/config"
	artdigest "github.com/fluxcd/pkg/artifact/digest"
	artsrv "github.com/fluxcd/pkg/artifact/server"
	artstore "github.com/fluxcd/pkg/artifact/storage"
	"github.com/fluxcd/pkg/runtime/client"
	"github.com/fluxcd/pkg/runtime/logger"
	"github.com/fluxcd/pkg/runtime/pprof"
	sourcev1 "github.com/fluxcd/source-controller/api/v1"

	swapi "github.com/fluxcd/source-watcher/api/v1beta1"
	"github.com/fluxcd/source-watcher/internal/controller"
	// +kubebuilder:scaffold:imports
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(sourcev1.AddToScheme(scheme))
	utilruntime.Must(swapi.AddToScheme(scheme))
	// +kubebuilder:scaffold:scheme
}

func main() {
	const controllerName = "source-watcher"

	var (
		metricsAddr          string
		healthAddr           string
		enableLeaderElection bool
		concurrent           int
		httpRetry            int
		requeueDependency    time.Duration
		artifactOptions      artcfg.Options
		clientOptions        client.Options
		logOptions           logger.Options
	)

	flag.IntVar(&concurrent, "concurrent", 10, "The number of concurrent resource reconciles.")
	flag.StringVar(&metricsAddr, "metrics-addr", ":8080", "The address the metric endpoint binds to.")
	flag.StringVar(&healthAddr, "health-addr", ":9440", "The address the health endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "enable-leader-election", false,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")
	flag.IntVar(&httpRetry, "http-retry", 9,
		"The maximum number of retries when failing to fetch artifacts over HTTP.")
	flag.DurationVar(&requeueDependency, "requeue-dependency", 5*time.Second,
		"The interval at which failing dependencies are reevaluated.")

	artifactOptions.BindFlags(flag.CommandLine)
	clientOptions.BindFlags(flag.CommandLine)
	logOptions.BindFlags(flag.CommandLine)

	flag.Parse()

	ctrl.SetLogger(logger.NewLogger(logOptions))

	algo, err := artdigest.AlgorithmForName(artifactOptions.ArtifactDigestAlgo)
	if err != nil {
		setupLog.Error(err, "unable to configure canonical digest algorithm")
		os.Exit(1)
	}
	artdigest.Canonical = algo

	storage, err := artstore.New(&artifactOptions)
	if err != nil {
		setupLog.Error(err, "unable to configure artifact storage")
		os.Exit(1)
	}
	setupLog.Info("storage setup for " + storage.BasePath)

	ctx := ctrl.SetupSignalHandler()
	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme: scheme,
		Metrics: metricsserver.Options{
			BindAddress:   metricsAddr,
			ExtraHandlers: pprof.GetHandlers(),
		},
		HealthProbeBindAddress:        healthAddr,
		LeaderElection:                enableLeaderElection,
		LeaderElectionID:              controllerName,
		LeaderElectionReleaseOnCancel: true,
		Controller: ctrlcfg.Controller{
			MaxConcurrentReconciles: concurrent,
			RecoverPanic:            ptr.To(true),
		},
		Client: ctrlclient.Options{
			Cache: &ctrlclient.CacheOptions{
				DisableFor: []ctrlclient.Object{&corev1.Secret{}, &corev1.ConfigMap{}},
			},
		},
	})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	if err = (&controller.ArtifactGeneratorReconciler{
		ControllerName:            controllerName,
		Client:                    mgr.GetClient(),
		APIReader:                 mgr.GetAPIReader(),
		Scheme:                    mgr.GetScheme(),
		EventRecorder:             mgr.GetEventRecorderFor(controllerName),
		Storage:                   storage,
		ArtifactFetchRetries:      httpRetry,
		DependencyRequeueInterval: requeueDependency,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", swapi.ArtifactGeneratorKind)
		os.Exit(1)
	}
	// +kubebuilder:scaffold:builder

	go func() {
		// Block until our controller manager is elected leader. We presume our
		// entire process will terminate if we lose leadership, so we don't need
		// to handle that.
		<-mgr.Elected()

		// Start the artifact server if running as leader.
		setupLog.Info("starting storage server on " + artifactOptions.StorageAddress)
		if err := artsrv.Start(ctx, &artifactOptions); err != nil {
			setupLog.Error(err, "artifact server error")
			os.Exit(1)
		}
	}()

	probes.SetupChecks(mgr, setupLog)

	setupLog.Info("starting manager")
	if err := mgr.Start(ctx); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}
