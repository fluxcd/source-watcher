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
	"fmt"
	"os"
	"time"

	flag "github.com/spf13/pflag"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	_ "k8s.io/client-go/plugin/pkg/client/auth"
	"k8s.io/utils/ptr"
	ctrlruntime "sigs.k8s.io/controller-runtime"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"
	ctrlcfg "sigs.k8s.io/controller-runtime/pkg/config"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/fluxcd/pkg/artifact/config"
	"github.com/fluxcd/pkg/artifact/digest"
	"github.com/fluxcd/pkg/artifact/server"
	"github.com/fluxcd/pkg/artifact/storage"
	"github.com/fluxcd/pkg/runtime/acl"
	"github.com/fluxcd/pkg/runtime/client"
	ctrl "github.com/fluxcd/pkg/runtime/controller"
	"github.com/fluxcd/pkg/runtime/features"
	"github.com/fluxcd/pkg/runtime/jitter"
	"github.com/fluxcd/pkg/runtime/leaderelection"
	"github.com/fluxcd/pkg/runtime/logger"
	"github.com/fluxcd/pkg/runtime/pprof"
	"github.com/fluxcd/pkg/runtime/probes"
	sourcev1 "github.com/fluxcd/source-controller/api/v1"

	swapi "github.com/fluxcd/source-watcher/api/v1beta1"
	"github.com/fluxcd/source-watcher/internal/controller"
	// +kubebuilder:scaffold:imports
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrlruntime.Log.WithName("setup")
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
		metricsAddr           string
		healthAddr            string
		concurrent            int
		httpRetry             int
		reconciliationTimeout time.Duration
		requeueDependency     time.Duration
		artifactOptions       config.Options
		aclOptions            acl.Options
		clientOptions         client.Options
		logOptions            logger.Options
		leaderElectionOptions leaderelection.Options
		rateLimiterOptions    ctrl.RateLimiterOptions
		intervalJitterOptions jitter.IntervalOptions
		featureGates          features.FeatureGates
	)

	flag.IntVar(&concurrent, "concurrent", 10, "The number of concurrent resource reconciles.")
	flag.StringVar(&metricsAddr, "metrics-addr", ":8080", "The address the metric endpoint binds to.")
	flag.StringVar(&healthAddr, "health-addr", ":9440", "The address the health endpoint binds to.")
	flag.IntVar(&httpRetry, "http-retry", 9,
		"The maximum number of retries when failing to fetch artifacts over HTTP.")
	flag.DurationVar(&reconciliationTimeout, "reconciliation-timeout", 10*time.Minute,
		"The maximum duration of a reconciliation.")
	flag.DurationVar(&requeueDependency, "requeue-dependency", 5*time.Second,
		"The interval at which failing dependencies are reevaluated.")

	artifactOptions.BindFlags(flag.CommandLine)
	aclOptions.BindFlags(flag.CommandLine)
	clientOptions.BindFlags(flag.CommandLine)
	logOptions.BindFlags(flag.CommandLine)
	leaderElectionOptions.BindFlags(flag.CommandLine)
	rateLimiterOptions.BindFlags(flag.CommandLine)
	intervalJitterOptions.BindFlags(flag.CommandLine)
	featureGates.BindFlags(flag.CommandLine)

	flag.Parse()

	ctrlruntime.SetLogger(logger.NewLogger(logOptions))

	digestAlgo, err := digest.AlgorithmForName(artifactOptions.ArtifactDigestAlgo)
	if err != nil {
		setupLog.Error(err, "unable to configure canonical digest algorithm")
		os.Exit(1)
	}
	digest.Canonical = digestAlgo

	artifactStorage, err := storage.New(&artifactOptions)
	if err != nil {
		setupLog.Error(err, "unable to configure artifact storage")
		os.Exit(1)
	}
	setupLog.Info("storage setup for " + artifactStorage.BasePath)

	if err := intervalJitterOptions.SetGlobalJitter(nil); err != nil {
		setupLog.Error(err, "unable to set global jitter")
		os.Exit(1)
	}

	ctx := ctrlruntime.SetupSignalHandler()
	leaderElectionID := fmt.Sprintf("%s-%s", controllerName, "leader-election")
	mgr, err := ctrlruntime.NewManager(ctrlruntime.GetConfigOrDie(), ctrlruntime.Options{
		Scheme: scheme,
		Metrics: metricsserver.Options{
			BindAddress:   metricsAddr,
			ExtraHandlers: pprof.GetHandlers(),
		},
		HealthProbeBindAddress:        healthAddr,
		LeaderElection:                leaderElectionOptions.Enable,
		LeaderElectionID:              leaderElectionID,
		LeaderElectionReleaseOnCancel: leaderElectionOptions.ReleaseOnCancel,
		LeaseDuration:                 &leaderElectionOptions.LeaseDuration,
		RenewDeadline:                 &leaderElectionOptions.RenewDeadline,
		Logger:                        ctrlruntime.Log,
		Controller: ctrlcfg.Controller{
			MaxConcurrentReconciles: concurrent,
			RecoverPanic:            ptr.To(true),
			ReconciliationTimeout:   reconciliationTimeout,
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

	// Note that the liveness check will pass beyond this point, but the readiness
	// check will continue to fail until this controller instance is elected leader.
	probes.SetupChecks(mgr, setupLog)

	if err = (&controller.ArtifactGeneratorReconciler{
		ControllerName:            controllerName,
		Client:                    mgr.GetClient(),
		APIReader:                 mgr.GetAPIReader(),
		Scheme:                    mgr.GetScheme(),
		EventRecorder:             mgr.GetEventRecorderFor(controllerName),
		Storage:                   artifactStorage,
		ArtifactFetchRetries:      httpRetry,
		DependencyRequeueInterval: requeueDependency,
		NoCrossNamespaceRefs:      aclOptions.NoCrossNamespaceRefs,
	}).SetupWithManager(ctx, mgr, controller.ArtifactGeneratorReconcilerOptions{
		RateLimiter: ctrl.GetRateLimiter(rateLimiterOptions),
	}); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", swapi.ArtifactGeneratorKind)
		os.Exit(1)
	}
	// +kubebuilder:scaffold:builder

	go func() {
		// Block until our controller manager is elected leader.We presume our
		// entire process will terminate if we lose leadership, so we don't need
		// to handle that.
		<-mgr.Elected()

		// Start the artifact server if running as leader.
		// This will make the readiness check pass and Kubernetes will start
		// routing traffic from kustomize-controller and helm-controller to this instance.
		setupLog.Info("starting storage server on " + artifactOptions.StorageAddress)
		if err := server.Start(ctx, &artifactOptions); err != nil {
			setupLog.Error(err, "artifact server error")
			os.Exit(1)
		}
	}()

	setupLog.Info("starting manager")
	if err := mgr.Start(ctx); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}
