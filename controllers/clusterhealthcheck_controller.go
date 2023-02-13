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
	"sync"

	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/source"

	libsveltosv1alpha1 "github.com/projectsveltos/libsveltos/api/v1alpha1"
	libsveltosset "github.com/projectsveltos/libsveltos/lib/set"
)

// ClusterHealthCheckReconciler reconciles a ClusterHealthCheck object
type ClusterHealthCheckReconciler struct {
	client.Client
	Scheme               *runtime.Scheme
	ConcurrentReconciles int

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

//+kubebuilder:rbac:groups=lib.projectsveltos.io.projectsveltos.io,resources=clusterhealthchecks,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=lib.projectsveltos.io.projectsveltos.io,resources=clusterhealthchecks/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=lib.projectsveltos.io.projectsveltos.io,resources=clusterhealthchecks/finalizers,verbs=update
//+kubebuilder:rbac:groups=cluster.x-k8s.io,resources=clusters,verbs=get;watch;list
//+kubebuilder:rbac:groups=cluster.x-k8s.io,resources=clusters/status,verbs=get;watch;list
//+kubebuilder:rbac:groups=cluster.x-k8s.io,resources=machines,verbs=get;watch;list
//+kubebuilder:rbac:groups=cluster.x-k8s.io,resources=machines/status,verbs=get;watch;list
//+kubebuilder:rbac:groups=lib.projectsveltos.io,resources=sveltosclusters,verbs=get;watch;list
//+kubebuilder:rbac:groups=lib.projectsveltos.io,resources=sveltosclusters/status,verbs=get;watch;list

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the ClusterHealthCheck object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.12.1/pkg/reconcile
func (r *ClusterHealthCheckReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	_ = log.FromContext(ctx)

	// TODO(user): your logic here

	return ctrl.Result{}, nil
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
	// one or more ClusterProfiles need to be reconciled.
	err = c.Watch(&source.Kind{Type: &libsveltosv1alpha1.SveltosCluster{}},
		handler.EnqueueRequestsFromMapFunc(r.requeueClusterProfileForCluster),
		SveltosClusterPredicates(mgr.GetLogger().WithValues("predicate", "sveltosclusterpredicate")),
	)

	return c, err
}

func (r *ClusterHealthCheckReconciler) WatchForCAPI(mgr ctrl.Manager, c controller.Controller) error {
	// When cluster-api cluster changes, according to ClusterPredicates,
	// one or more ClusterProfiles need to be reconciled.
	if err := c.Watch(&source.Kind{Type: &clusterv1.Cluster{}},
		handler.EnqueueRequestsFromMapFunc(r.requeueClusterProfileForCluster),
		ClusterPredicates(mgr.GetLogger().WithValues("predicate", "clusterpredicate")),
	); err != nil {
		return err
	}

	return nil
}
