/*
Copyright 2023.

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
	"context"
	"flag"
	"fmt"
	"os"
	"sync"
	"syscall"
	"time"

	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.)
	// to ensure that exec-entrypoint and run can make use of them.
	_ "k8s.io/client-go/plugin/pkg/client/auth"

	"github.com/go-logr/logr"
	"github.com/spf13/pflag"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	cliflag "k8s.io/component-base/cli/flag"
	"k8s.io/klog/v2"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/metrics/filters"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
	"github.com/projectsveltos/libsveltos/lib/crd"
	"github.com/projectsveltos/libsveltos/lib/deployer"
	"github.com/projectsveltos/libsveltos/lib/logsettings"
	libsveltosset "github.com/projectsveltos/libsveltos/lib/set"

	"github.com/projectsveltos/healthcheck-manager/controllers"
	//+kubebuilder:scaffold:imports
)

var (
	setupLog                     = ctrl.Log.WithName("setup")
	shardKey                     string
	version                      string
	diagnosticsAddress           string
	insecureDiagnostics          bool
	agentInMgmtCluster           bool
	workers                      int
	concurrentReconciles         int
	reportMode                   controllers.ReportMode
	tmpReportMode                int
	reloaderReportCollectionTime int
	restConfigQPS                float32
	restConfigBurst              int
	webhookPort                  int
	syncPeriod                   time.Duration
	healthAddr                   string
	capiOnboardAnnotation        string
)

const (
	defaultReconcilers        = 10
	defaultWorkers            = 20
	defaultReloaderReportTime = 10 // time is in second
	defaulReportMode          = int(controllers.CollectFromManagementCluster)
)

// Add RBAC for the authorized diagnostics endpoint.
//+kubebuilder:rbac:groups=authentication.k8s.io,resources=tokenreviews,verbs=create
//+kubebuilder:rbac:groups=authorization.k8s.io,resources=subjectaccessreviews,verbs=create

func main() {
	scheme, err := controllers.InitScheme()
	if err != nil {
		os.Exit(1)
	}

	klog.InitFlags(nil)

	initFlags(pflag.CommandLine)
	pflag.CommandLine.SetNormalizeFunc(cliflag.WordSepNormalizeFunc)
	pflag.CommandLine.AddGoFlagSet(flag.CommandLine)
	pflag.Parse()

	reportMode = controllers.ReportMode(tmpReportMode)

	ctrl.SetLogger(klog.Background())

	ctrlOptions := ctrl.Options{
		Scheme:                 scheme,
		Metrics:                getDiagnosticsOptions(),
		HealthProbeBindAddress: healthAddr,
		WebhookServer: webhook.NewServer(
			webhook.Options{
				Port: webhookPort,
			}),
		Cache: cache.Options{
			SyncPeriod: &syncPeriod,
		},
	}

	restConfig := ctrl.GetConfigOrDie()
	restConfig.QPS = restConfigQPS
	restConfig.Burst = restConfigBurst

	mgr, err := ctrl.NewManager(restConfig, ctrlOptions)
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	controllers.SetManagementClusterAccess(mgr.GetClient(), mgr.GetConfig())
	controllers.SetAgentInMgmtCluster(agentInMgmtCluster)

	// Setup the context that's going to be used in controllers and for the manager.
	ctx := ctrl.SetupSignalHandler()

	logsettings.RegisterForLogSettings(ctx,
		libsveltosv1beta1.ComponentHealthCheckManager, ctrl.Log.WithName("log-setter"),
		ctrl.GetConfigOrDie())

	d := deployer.GetClient(ctx, ctrl.Log.WithName("deployer"), mgr.GetClient(), workers)
	controllers.RegisterFeatures(d, setupLog)

	controllers.SetManagementRecorder(mgr.GetEventRecorderFor("notification-recorder"))

	var clusterHealthCheckController controller.Controller
	clusterHealthCheckReconciler := getClusterHealthCheckReconciler(mgr)
	clusterHealthCheckReconciler.Deployer = d

	clusterHealthCheckController, err = clusterHealthCheckReconciler.SetupWithManager(mgr)
	if err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "ClusterHealthCheck")
		os.Exit(1)
	}
	if err = (getHealthCheckReconciler(mgr)).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "HealthCheck")
		os.Exit(1)
	}

	if err = (&controllers.SveltosClusterReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "SveltosCluster")
		os.Exit(1)
	}
	if err = (getReloaderReportReconciler(mgr)).SetupWithManager(mgr, reloaderReportCollectionTime); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "ReloaderReport")
		os.Exit(1)
	}
	//+kubebuilder:scaffold:builder

	setupChecks(mgr)

	go capiWatchers(ctx, mgr,
		clusterHealthCheckReconciler, clusterHealthCheckController,
		setupLog)

	setupLog.Info("starting manager")
	if err := mgr.Start(ctx); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}

func initFlags(fs *pflag.FlagSet) {
	fs.BoolVar(&agentInMgmtCluster, "agent-in-mgmt-cluster", false,
		"When set, indicates drift-detection-manager needs to be started in the management cluster")

	fs.IntVar(&tmpReportMode, "report-mode", defaulReportMode,
		"Indicates how ClassifierReport needs to be collected")

	fs.IntVar(&reloaderReportCollectionTime, "reloaderreport-time", defaultReloaderReportTime,
		"Interval, in seconds, at which ReloaderReports are collected from managed cluster.")

	fs.StringVar(&diagnosticsAddress, "diagnostics-address", ":8443",
		"The address the diagnostics endpoint binds to. Per default metrics are served via https and with"+
			"authentication/authorization. To serve via http and without authentication/authorization set --insecure-diagnostics."+
			"If --insecure-diagnostics is not set the diagnostics endpoint also serves pprof endpoints and an endpoint to change the log level.")

	fs.BoolVar(&insecureDiagnostics, "insecure-diagnostics", false,
		"Enable insecure diagnostics serving. For more details see the description of --diagnostics-address.")

	fs.StringVar(&shardKey, "shard-key", "",
		"If set, and report-mode is set to collect, this deployment will fetch only from clusters matching this shard")

	fs.StringVar(&capiOnboardAnnotation, "capi-onboard-annotation", "",
		"If provided, Sveltos will only manage CAPI clusters that have this exact annotation.")

	fs.StringVar(&version, "version", "", "current sveltos version")

	fs.IntVar(&workers, "worker-number", defaultWorkers,
		"Number of worker. Workers are used to verify health checks in managed clusters")

	fs.IntVar(&concurrentReconciles, "concurrent-reconciles", defaultReconcilers,
		"concurrent reconciles is the maximum number of concurrent Reconciles which can be run. Defaults to 10")

	fs.StringVar(&healthAddr, "health-addr", ":9440",
		"The address the health endpoint binds to.")

	const defautlRestConfigQPS = 20
	fs.Float32Var(&restConfigQPS, "kube-api-qps", defautlRestConfigQPS,
		fmt.Sprintf("Maximum queries per second from the controller client to the Kubernetes API server. Defaults to %d",
			defautlRestConfigQPS))

	const defaultRestConfigBurst = 30
	fs.IntVar(&restConfigBurst, "kube-api-burst", defaultRestConfigBurst,
		fmt.Sprintf("Maximum number of queries that should be allowed in one burst from the controller client to the Kubernetes API server. Default %d",
			defaultRestConfigBurst))

	const defaultWebhookPort = 9443
	fs.IntVar(&webhookPort, "webhook-port", defaultWebhookPort,
		"Webhook Server port")

	const defaultSyncPeriod = 10
	fs.DurationVar(&syncPeriod, "sync-period", defaultSyncPeriod*time.Minute,
		fmt.Sprintf("The minimum interval at which watched resources are reconciled (e.g. 15m). Default: %d minutes",
			defaultSyncPeriod))
}

func setupChecks(mgr ctrl.Manager) {
	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up ready check")
		os.Exit(1)
	}
}

// capiCRDHandler restarts process if a CAPI CRD is updated
func capiCRDHandler(gvk *schema.GroupVersionKind, action crd.ChangeType) {
	if action == crd.Modify {
		return
	}
	if gvk.Group == clusterv1.GroupVersion.Group {
		if killErr := syscall.Kill(syscall.Getpid(), syscall.SIGTERM); killErr != nil {
			panic("kill -TERM failed")
		}
	}
}

// isCAPIInstalled returns true if CAPI is installed, false otherwise
func isCAPIInstalled(ctx context.Context, c client.Client) (bool, error) {
	clusterCRD := &apiextensionsv1.CustomResourceDefinition{}

	err := c.Get(ctx, types.NamespacedName{Name: "clusters.cluster.x-k8s.io"}, clusterCRD)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}

	return true, nil
}

func capiWatchers(ctx context.Context, mgr ctrl.Manager, clusterHealthCheckReconciler *controllers.ClusterHealthCheckReconciler,
	clusterHealthCheckController controller.Controller, logger logr.Logger) {

	const maxRetries = 20
	retries := 0
	for {
		capiPresent, err := isCAPIInstalled(ctx, mgr.GetClient())
		if err != nil {
			if retries < maxRetries {
				logger.Info(fmt.Sprintf("failed to verify if CAPI is present: %v", err))
				time.Sleep(time.Second)
			}
			retries++
		} else {
			if !capiPresent {
				setupLog.V(logsettings.LogInfo).Info("CAPI currently not present. Starting CRD watcher")
				go crd.WatchCustomResourceDefinition(ctx, mgr.GetConfig(), capiCRDHandler, setupLog)
			} else {
				setupLog.V(logsettings.LogInfo).Info("CAPI present.")
				err = clusterHealthCheckReconciler.WatchForCAPI(mgr, clusterHealthCheckController)
				if err != nil {
					continue
				}
				if err = (&controllers.ClusterReconciler{
					Client: mgr.GetClient(),
					Scheme: mgr.GetScheme(),
				}).SetupWithManager(mgr); err != nil {
					logger.Error(err, "unable to create controller", "controller", "Cluster")
					continue
				}
			}
			return
		}
	}
}

func getClusterHealthCheckReconciler(mgr manager.Manager) *controllers.ClusterHealthCheckReconciler {
	return &controllers.ClusterHealthCheckReconciler{
		Client:                mgr.GetClient(),
		Scheme:                mgr.GetScheme(),
		ConcurrentReconciles:  concurrentReconciles,
		Mux:                   sync.Mutex{},
		ShardKey:              shardKey,
		CapiOnboardAnnotation: capiOnboardAnnotation,
		ClusterMap:            make(map[corev1.ObjectReference]*libsveltosset.Set),
		CHCToClusterMap:       make(map[types.NamespacedName]*libsveltosset.Set),
		ClusterHealthChecks:   make(map[corev1.ObjectReference]libsveltosv1beta1.Selector),
		ClusterLabels:         make(map[corev1.ObjectReference]map[string]string),
		HealthCheckMap:        make(map[corev1.ObjectReference]*libsveltosset.Set),
		CHCToHealthCheckMap:   make(map[types.NamespacedName]*libsveltosset.Set),
	}
}

// getDiagnosticsOptions returns metrics options which can be used to configure a Manager.
func getDiagnosticsOptions() metricsserver.Options {
	// If "--insecure-diagnostics" is set, serve metrics via http
	// and without authentication/authorization.
	if insecureDiagnostics {
		return metricsserver.Options{
			BindAddress:   diagnosticsAddress,
			SecureServing: false,
		}
	}

	// If "--insecure-diagnostics" is not set, serve metrics via https
	// and with authentication/authorization. As the endpoint is protected,
	// we also serve pprof endpoints and an endpoint to change the log level.
	return metricsserver.Options{
		BindAddress:    diagnosticsAddress,
		SecureServing:  true,
		FilterProvider: filters.WithAuthenticationAndAuthorization,
	}
}

func getHealthCheckReconciler(mgr manager.Manager) *controllers.HealthCheckReconciler {
	return &controllers.HealthCheckReconciler{
		Client:                mgr.GetClient(),
		Scheme:                mgr.GetScheme(),
		HealthCheckReportMode: reportMode,
		ShardKey:              shardKey,
		CapiOnboardAnnotation: capiOnboardAnnotation,
		Version:               version,
	}
}

func getReloaderReportReconciler(mgr manager.Manager) *controllers.ReloaderReportReconciler {
	return &controllers.ReloaderReportReconciler{
		Client:                mgr.GetClient(),
		Scheme:                mgr.GetScheme(),
		ReloaderReportMode:    reportMode,
		ShardKey:              shardKey,
		CapiOnboardAnnotation: capiOnboardAnnotation,
		Version:               version,
	}
}
