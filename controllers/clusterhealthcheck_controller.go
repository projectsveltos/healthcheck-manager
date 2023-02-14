/*
Copyright 2023. projectsveltos.io. All rights reserved.

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

package controllers

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	"github.com/projectsveltos/healthcheck-manager/pkg/scope"
	libsveltosv1alpha1 "github.com/projectsveltos/libsveltos/api/v1alpha1"
	"github.com/projectsveltos/libsveltos/lib/deployer"
	logs "github.com/projectsveltos/libsveltos/lib/logsettings"
	libsveltosset "github.com/projectsveltos/libsveltos/lib/set"
	configv1alpha1 "github.com/projectsveltos/sveltos-manager/api/v1alpha1"
)

const (
	// deleteRequeueAfter is how long to wait before checking again to see if the cluster still has
	// children during deletion.
	deleteRequeueAfter = 20 * time.Second

	// normalRequeueAfter is how long to wait before checking again to see if the cluster can be moved
	// to ready after or workload features (for instance ingress or reporter) have failed
	normalRequeueAfter = 20 * time.Second
)

// ClusterHealthCheckReconciler reconciles a ClusterHealthCheck object
type ClusterHealthCheckReconciler struct {
	client.Client
	Scheme               *runtime.Scheme
	ConcurrentReconciles int
	Deployer             deployer.DeployerInterface
	// use a Mutex to update Map as MaxConcurrentReconciles is higher than one
	Mux sync.Mutex

	// key: Sveltos/CAPI Cluster: value: set of all ClusterHealthCheck instances matching the Cluster
	ClusterMap map[corev1.ObjectReference]*libsveltosset.Set

	// key: ClusterHealthCheck: value: set of Sveltos/CAPI Clusters matched
	ClusterHealthCheckMap map[corev1.ObjectReference]*libsveltosset.Set
	// key: ClusterHealthCheck; value ClusterHealthCheck Selector
	ClusterHealthChecks map[corev1.ObjectReference]libsveltosv1alpha1.Selector

	// For each cluster contains current labels
	// This is needed in following scenario:
	// - ClusterHealthCheck is created
	// - Cluster is created with labels matching ClusterHealthCheck
	// - When first control plane machine in such cluster becomes available
	// we need Cluster labels to know which ClusterHealthCheck to reconcile
	ClusterLabels map[corev1.ObjectReference]map[string]string

	// Reason for the two maps:
	// ClusterHealthCheck, via ClusterSelector, matches Sveltos/CAPI Clusters based on Cluster labels.
	// When a Sveltos/CAPI Cluster labels change, one or more ClusterHealthChecks need to be reconciled.
	// In order to achieve so, ClusterHealthCheck reconciler watches for Sveltos/CAPI Clusters. When a Sveltos/CAPI Cluster
	// label changes, find all the ClusterHealthChecks currently referencing it and reconcile those.
	// Problem is no I/O should be present inside a MapFunc (given a Sveltos/CAPI Cluster, return all the ClusterHealthChecks matching it).
	// In the MapFunc, if the list ClusterHealthChecks operation failed, we would be unable to retry or re-enqueue the rigth set of
	// ClusterHealthChecks.
	// Instead the approach taken is following:
	// - when a ClusterHealthCheck is reconciled, update the ClusterHealthChecks amd the ClusterMap;
	// - in the MapFunc, given the Sveltos/CAPI Cluster that changed:
	//		* use ClusterHealthChecks to find all ClusterHealthCheck matching the Cluster and reconcile those;
	//      * use ClusterMap to reconcile all ClusterHealthChecks previoulsy matching the Cluster.
	//
	// The ClusterHealthCheckMap is used to update ClusterMap. Consider following scenarios to understand the need:
	// 1. ClusterHealthCheck A references Clusters 1 and 2. When reconciled, ClusterMap will have 1 => A and 2 => A;
	// and ClusterHealthCheckMap A => 1,2
	// 2. Cluster 2 label changes and now ClusterHealthCheck matches Cluster 1 only. We ned to remove the entry 2 => A in ClusterMap. But
	// when we reconcile ClusterHealthCheck we have its current version we don't have its previous version. So we know ClusterHealthCheck A
	// now matches Sveltos/CAPI Cluster 1, but we don't know it used to match Sveltos/CAPI Cluster 2.
	// So we use ClusterHealthCheckMap (at this point value stored here corresponds to reconciliation #1. We know currently
	// ClusterHealthCheck matches Sveltos/CAPI Cluster 1 only and looking at ClusterHealthCheckMap we know it used to reference
	// Svetos/CAPI Cluster 1 and 2.
	// So we can remove 2 => A from ClusterMap. Only after this update, we update ClusterHealthCheckMap (so new value will be A => 1)
}

//+kubebuilder:rbac:groups=lib.projectsveltos.io,resources=clusterhealthchecks,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=lib.projectsveltos.io,resources=clusterhealthchecks/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=lib.projectsveltos.io,resources=clusterhealthchecks/finalizers,verbs=update
//+kubebuilder:rbac:groups=config.projectsveltos.io,resources=clustersummaries,verbs=get;list;watch
//+kubebuilder:rbac:groups=config.projectsveltos.io,resources=clustersummaries/status,verbs=get;list;watch
//+kubebuilder:rbac:groups="",resources=events,verbs=get;watch;list;create;update
//+kubebuilder:rbac:groups=cluster.x-k8s.io,resources=clusters,verbs=get;watch;list
//+kubebuilder:rbac:groups=cluster.x-k8s.io,resources=clusters/status,verbs=get;watch;list
//+kubebuilder:rbac:groups=cluster.x-k8s.io,resources=machines,verbs=get;watch;list
//+kubebuilder:rbac:groups=cluster.x-k8s.io,resources=machines/status,verbs=get;watch;list
//+kubebuilder:rbac:groups=lib.projectsveltos.io,resources=sveltosclusters,verbs=get;watch;list
//+kubebuilder:rbac:groups=lib.projectsveltos.io,resources=sveltosclusters/status,verbs=get;watch;list

func (r *ClusterHealthCheckReconciler) Reconcile(ctx context.Context, req ctrl.Request) (_ ctrl.Result, reterr error) {
	logger := ctrl.LoggerFrom(ctx)
	logger.V(logs.LogInfo).Info("Reconciling")

	// Fecth the ClusterHealthCheck instance
	clusterHealthCheck := &libsveltosv1alpha1.ClusterHealthCheck{}
	if err := r.Get(ctx, req.NamespacedName, clusterHealthCheck); err != nil {
		if apierrors.IsNotFound(err) {
			return reconcile.Result{}, nil
		}
		logger.Error(err, "Failed to fetch ClusterHealthCheck")
		return reconcile.Result{}, errors.Wrapf(
			err,
			"Failed to fetch ClusterHealthCheck %s",
			req.NamespacedName,
		)
	}

	clusterHealthCheckScope, err := scope.NewClusterHealthCheckScope(scope.ClusterHealthCheckScopeParams{
		Client:             r.Client,
		Logger:             logger,
		ClusterHealthCheck: clusterHealthCheck,
		ControllerName:     "clusterHealthCheck",
	})
	if err != nil {
		logger.Error(err, "Failed to create clusterHealthCheckScope")
		return reconcile.Result{}, errors.Wrapf(
			err,
			"unable to create clusterHealthCheck scope for %s",
			req.NamespacedName,
		)
	}

	// Always close the scope when exiting this function so we can persist any ClusterHealthCheck
	// changes.
	defer func() {
		if err := clusterHealthCheckScope.Close(ctx); err != nil {
			reterr = err
		}
	}()

	// Handle deleted clusterHealthCheck
	if !clusterHealthCheck.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, clusterHealthCheckScope)
	}

	// Handle non-deleted clusterHealthCheck
	return r.reconcileNormal(ctx, clusterHealthCheckScope)
}

func (r *ClusterHealthCheckReconciler) reconcileDelete(
	ctx context.Context,
	clusterHealthCheckScope *scope.ClusterHealthCheckScope,
) (reconcile.Result, error) {

	logger := clusterHealthCheckScope.Logger
	logger.V(logs.LogInfo).Info("Reconciling ClusterHealthCheck delete")

	clusterHealthCheckScope.SetMatchingClusterRefs(nil)

	r.updateMaps(clusterHealthCheckScope)

	f := getHandlersForFeature(libsveltosv1alpha1.FeatureClusterHealthCheck)
	err := r.undeployClusterHealthCheck(ctx, clusterHealthCheckScope, f, logger)
	if err != nil {
		logger.V(logs.LogInfo).Error(err, "failed to undeploy")
		return reconcile.Result{Requeue: true, RequeueAfter: deleteRequeueAfter}, nil
	}

	if controllerutil.ContainsFinalizer(clusterHealthCheckScope.ClusterHealthCheck, libsveltosv1alpha1.ClusterHealthCheckFinalizer) {
		controllerutil.RemoveFinalizer(clusterHealthCheckScope.ClusterHealthCheck, libsveltosv1alpha1.ClusterHealthCheckFinalizer)
	}

	logger.V(logs.LogInfo).Info("Reconcile delete success")
	return reconcile.Result{}, nil
}

func (r *ClusterHealthCheckReconciler) reconcileNormal(
	ctx context.Context,
	clusterHealthCheckScope *scope.ClusterHealthCheckScope,
) (reconcile.Result, error) {

	logger := clusterHealthCheckScope.Logger
	logger.V(logs.LogInfo).Info("Reconciling ClusterHealthCheck")

	if !controllerutil.ContainsFinalizer(clusterHealthCheckScope.ClusterHealthCheck, libsveltosv1alpha1.ClusterHealthCheckFinalizer) {
		if err := r.addFinalizer(ctx, clusterHealthCheckScope); err != nil {
			return reconcile.Result{}, err
		}
	}

	parsedSelector, _ := labels.Parse(clusterHealthCheckScope.GetSelector())
	matchingCluster, err := getMatchingClusters(ctx, r.Client, parsedSelector, clusterHealthCheckScope.Logger)
	if err != nil {
		return reconcile.Result{}, err
	}

	clusterHealthCheckScope.SetMatchingClusterRefs(matchingCluster)

	err = r.updateClusterConditions(ctx, clusterHealthCheckScope)
	if err != nil {
		logger.V(logs.LogDebug).Info("failed to update clusterConditions")
		return reconcile.Result{}, err
	}

	r.updateMaps(clusterHealthCheckScope)

	f := getHandlersForFeature(libsveltosv1alpha1.FeatureClusterHealthCheck)
	if err := r.deployClusterHealthCheck(ctx, clusterHealthCheckScope, f, logger); err != nil {
		logger.V(logs.LogInfo).Error(err, "failed to deploy")
		return reconcile.Result{Requeue: true, RequeueAfter: normalRequeueAfter}, nil
	}

	logger.V(logs.LogInfo).Info("Reconcile success")
	return reconcile.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *ClusterHealthCheckReconciler) SetupWithManager(mgr ctrl.Manager) (controller.Controller, error) {
	c, err := ctrl.NewControllerManagedBy(mgr).
		For(&libsveltosv1alpha1.ClusterHealthCheck{}).
		WithOptions(controller.Options{
			MaxConcurrentReconciles: r.ConcurrentReconciles,
		}).
		Build(r)
	if err != nil {
		return nil, errors.Wrap(err, "error creating controller")
	}
	// When projectsveltos cluster changes, according to SveltosClusterPredicates,
	// one or more ClusterHealthChecks need to be reconciled.
	err = c.Watch(&source.Kind{Type: &libsveltosv1alpha1.SveltosCluster{}},
		handler.EnqueueRequestsFromMapFunc(r.requeueClusterHealthCheckForCluster),
		SveltosClusterPredicates(mgr.GetLogger().WithValues("predicate", "sveltosclusterpredicate")),
	)
	if err != nil {
		return nil, errors.Wrap(err, "error creating controller")
	}

	// When projectsveltos clusterSummary changes, according to ClusterSummaryPredicates,
	// one or more ClusterHealthChecks need to be reconciled.
	err = c.Watch(&source.Kind{Type: &configv1alpha1.ClusterSummary{}},
		handler.EnqueueRequestsFromMapFunc(r.requeueClusterHealthCheckForClusterSummary),
		ClusterSummaryPredicates(mgr.GetLogger().WithValues("predicate", "clustersummarypredicate")),
	)

	return c, err
}

func (r *ClusterHealthCheckReconciler) WatchForCAPI(mgr ctrl.Manager, c controller.Controller) error {
	// When cluster-api cluster changes, according to ClusterPredicates,
	// one or more ClusterHealthChecks need to be reconciled.
	if err := c.Watch(&source.Kind{Type: &clusterv1.Cluster{}},
		handler.EnqueueRequestsFromMapFunc(r.requeueClusterHealthCheckForCluster),
		ClusterPredicates(mgr.GetLogger().WithValues("predicate", "clusterpredicate")),
	); err != nil {
		return err
	}

	// When cluster-api machine changes, according to ClusterPredicates,
	// one or more ClusterHealthCheck need to be reconciled.
	if err := c.Watch(&source.Kind{Type: &clusterv1.Machine{}},
		handler.EnqueueRequestsFromMapFunc(r.requeueClusterHealthCheckForMachine),
		MachinePredicates(mgr.GetLogger().WithValues("predicate", "machinepredicate")),
	); err != nil {
		return err
	}

	return nil
}

func (r *ClusterHealthCheckReconciler) addFinalizer(ctx context.Context, clusterHealthCheckScope *scope.ClusterHealthCheckScope) error {
	controllerutil.AddFinalizer(clusterHealthCheckScope.ClusterHealthCheck, libsveltosv1alpha1.ClusterHealthCheckFinalizer)
	// Register the finalizer immediately to avoid orphaning clusterHealthCheck resources on delete
	if err := clusterHealthCheckScope.PatchObject(ctx); err != nil {
		clusterHealthCheckScope.Error(err, "Failed to add finalizer")
		return errors.Wrapf(
			err,
			"Failed to add finalizer for %s",
			clusterHealthCheckScope.Name(),
		)
	}
	return nil
}

func (r *ClusterHealthCheckReconciler) updateMaps(clusterHealthCheckScope *scope.ClusterHealthCheckScope) {
	currentClusters := &libsveltosset.Set{}
	for i := range clusterHealthCheckScope.ClusterHealthCheck.Status.MatchingClusterRefs {
		cluster := clusterHealthCheckScope.ClusterHealthCheck.Status.MatchingClusterRefs[i]
		clusterInfo := &corev1.ObjectReference{
			Namespace: cluster.Namespace, Name: cluster.Name,
			Kind: cluster.Kind, APIVersion: cluster.APIVersion,
		}
		currentClusters.Insert(clusterInfo)
	}

	r.Mux.Lock()
	defer r.Mux.Unlock()

	clusterHealthCheckInfo := getKeyFromObject(r.Scheme, clusterHealthCheckScope.ClusterHealthCheck)

	// Get list of Clusters not matched anymore by ClusterHealthCheck
	var toBeRemoved []corev1.ObjectReference
	if v, ok := r.ClusterHealthCheckMap[*clusterHealthCheckInfo]; ok {
		toBeRemoved = v.Difference(currentClusters)
	}

	// For each currently matching Cluster, add ClusterHealthCheck as consumer
	for i := range clusterHealthCheckScope.ClusterHealthCheck.Status.MatchingClusterRefs {
		cluster := clusterHealthCheckScope.ClusterHealthCheck.Status.MatchingClusterRefs[i]
		clusterInfo := &corev1.ObjectReference{Namespace: cluster.Namespace, Name: cluster.Name, Kind: cluster.Kind, APIVersion: cluster.APIVersion}
		r.getClusterMapForEntry(clusterInfo).Insert(clusterHealthCheckInfo)
	}

	// For each Cluster not matched anymore, remove ClusterHealthCheck as consumer
	for i := range toBeRemoved {
		clusterName := toBeRemoved[i]
		r.getClusterMapForEntry(&clusterName).Erase(clusterHealthCheckInfo)
	}

	// Update list of Clusters currently referenced by ClusterHealthCheck instance
	r.ClusterHealthCheckMap[*clusterHealthCheckInfo] = currentClusters
	r.ClusterHealthChecks[*clusterHealthCheckInfo] = clusterHealthCheckScope.ClusterHealthCheck.Spec.ClusterSelector
}

func (r *ClusterHealthCheckReconciler) getClusterMapForEntry(entry *corev1.ObjectReference) *libsveltosset.Set {
	s := r.ClusterMap[*entry]
	if s == nil {
		s = &libsveltosset.Set{}
		r.ClusterMap[*entry] = s
	}
	return s
}

// updateClusterConditions updates ClusterHealthCheck Status ClusterConditions by adding an entry for any
// new cluster matching ClusterHealthCheck instance
func (r *ClusterHealthCheckReconciler) updateClusterConditions(ctx context.Context,
	clusterHealthCheckScope *scope.ClusterHealthCheckScope) error {

	chc := clusterHealthCheckScope.ClusterHealthCheck

	getClusterID := func(cluster corev1.ObjectReference) string {
		return fmt.Sprintf("%s:%s/%s", getClusterType(&cluster), cluster.Namespace, cluster.Name)
	}

	// Build Map for all Clusters with an entry in Classifier.Status.ClusterInfo
	clusterMap := make(map[string]bool)
	for i := range chc.Status.ClusterConditions {
		c := &chc.Status.ClusterConditions[i]
		clusterMap[getClusterID(c.ClusterInfo.Cluster)] = true
	}

	newClusterInfo := make([]libsveltosv1alpha1.ClusterCondition, 0)
	for i := range chc.Status.MatchingClusterRefs {
		c := chc.Status.MatchingClusterRefs[i]
		if _, ok := clusterMap[getClusterID(c)]; !ok {
			newClusterInfo = append(newClusterInfo,
				libsveltosv1alpha1.ClusterCondition{
					ClusterInfo: libsveltosv1alpha1.ClusterInfo{
						Cluster: c,
						Hash:    nil,
					},
				})
		}
	}

	finalClusterInfo := chc.Status.ClusterConditions
	finalClusterInfo = append(finalClusterInfo, newClusterInfo...)

	clusterHealthCheckScope.SetClusterConditions(finalClusterInfo)
	return nil
}
