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
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/klog/v2/klogr"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	libsveltosv1alpha1 "github.com/projectsveltos/libsveltos/api/v1alpha1"
	logs "github.com/projectsveltos/libsveltos/lib/logsettings"
	configv1alpha1 "github.com/projectsveltos/sveltos-manager/api/v1alpha1"
)

func (r *ClusterHealthCheckReconciler) requeueClusterHealthCheckForCluster(
	o client.Object,
) []reconcile.Request {

	cluster := o
	logger := klogr.New().WithValues(
		"objectMapper",
		"requeueClusterHealthCheckForCluster",
		"namespace",
		cluster.GetNamespace(),
		"cluster",
		cluster.GetName(),
	)

	logger.V(logs.LogDebug).Info("reacting to Cluster change")

	r.Mux.Lock()
	defer r.Mux.Unlock()

	addTypeInformationToObject(r.Scheme, cluster)

	apiVersion, kind := cluster.GetObjectKind().GroupVersionKind().ToAPIVersionAndKind()

	clusterInfo := corev1.ObjectReference{APIVersion: apiVersion, Kind: kind,
		Namespace: cluster.GetNamespace(), Name: cluster.GetName()}

	r.ClusterLabels[clusterInfo] = o.GetLabels()

	// Get all ClusterHealthChecks previously matching this cluster and reconcile those
	requests := make([]ctrl.Request, r.getClusterMapForEntry(&clusterInfo).Len())
	consumers := r.getClusterMapForEntry(&clusterInfo).Items()

	for i := range consumers {
		l := logger.WithValues("clusterHealthCheck", consumers[i].Name)
		l.V(logs.LogDebug).Info("queuing ClusterHealthCheck")
		requests[i] = ctrl.Request{
			NamespacedName: client.ObjectKey{
				Name: consumers[i].Name,
			},
		}
	}

	// Iterate over all current ClusterHealthCheck and reconcile the ClusterHealthCheck now
	// matching the Cluster
	for k := range r.ClusterHealthChecks {
		clusterHealthCheckSelector := r.ClusterHealthChecks[k]
		parsedSelector, _ := labels.Parse(string(clusterHealthCheckSelector))
		if parsedSelector.Matches(labels.Set(cluster.GetLabels())) {
			l := logger.WithValues("clusterHealthCheck", k.Name)
			l.V(logs.LogDebug).Info("queuing ClusterHealthCheck")
			requests = append(requests, ctrl.Request{
				NamespacedName: client.ObjectKey{
					Name: k.Name,
				},
			})
		}
	}

	return requests
}

func (r *ClusterHealthCheckReconciler) requeueClusterHealthCheckForClusterSummary(
	o client.Object,
) []reconcile.Request {

	clusterSummary := o
	logger := klogr.New().WithValues(
		"objectMapper",
		"requeueClusterHealthCheckForClusterSummary",
		"namespace",
		clusterSummary.GetNamespace(),
		"clustersummary",
		clusterSummary.GetName(),
	)

	logger.V(logs.LogDebug).Info("reacting to clusterSummary change")

	r.Mux.Lock()
	defer r.Mux.Unlock()

	addTypeInformationToObject(r.Scheme, clusterSummary)

	csLabels := clusterSummary.GetLabels()
	if csLabels == nil {
		logger.V(logs.LogInfo).Info("clustersummary has no label. Cannot reconcile.")
		return nil
	}

	clusterName, ok := csLabels[configv1alpha1.ClusterNameLabel]
	if !ok {
		logger.V(logs.LogInfo).Info("clustersummary has no label with cluster name. Cannot reconcile.")
		return nil
	}

	clusterType, ok := csLabels[configv1alpha1.ClusterTypeLabel]
	if !ok {
		logger.V(logs.LogInfo).Info("clustersummary has no label with cluster type. Cannot reconcile.")
		return nil
	}

	kind := libsveltosv1alpha1.SveltosClusterKind
	apiVersion := libsveltosv1alpha1.GroupVersion.String()

	if clusterType == string(libsveltosv1alpha1.ClusterTypeCapi) {
		kind = "Cluster"
		apiVersion = clusterv1.GroupVersion.String()
	}

	clusterInfo := corev1.ObjectReference{APIVersion: apiVersion, Kind: kind,
		Namespace: clusterSummary.GetNamespace(), Name: clusterName}

	logger = logger.WithValues("clustersummary", fmt.Sprintf("%s:%s/%s",
		kind, clusterSummary.GetNamespace(), clusterName))
	logger.V(logs.LogDebug).Info("get clusterhealthchecks for cluster")

	r.ClusterLabels[clusterInfo] = o.GetLabels()

	// Get all ClusterHealthChecks previously matching this cluster and reconcile those
	requests := make([]ctrl.Request, r.getClusterMapForEntry(&clusterInfo).Len())
	consumers := r.getClusterMapForEntry(&clusterInfo).Items()

	for i := range consumers {
		l := logger.WithValues("clusterHealthCheck", consumers[i].Name)
		l.V(logs.LogDebug).Info("queuing ClusterHealthCheck")
		requests[i] = ctrl.Request{
			NamespacedName: client.ObjectKey{
				Name: consumers[i].Name,
			},
		}
	}

	// Get Cluster labels
	if clusterLabels, ok := r.ClusterLabels[clusterInfo]; ok {
		// Iterate over all current ClusterHealthCheck and reconcile the ClusterHealthCheck now
		// matching the Cluster
		for k := range r.ClusterHealthChecks {
			clusterHealthCheckSelector := r.ClusterHealthChecks[k]
			parsedSelector, _ := labels.Parse(string(clusterHealthCheckSelector))
			if parsedSelector.Matches(labels.Set(clusterLabels)) {
				l := logger.WithValues("clusterHealthCheck", k.Name)
				l.V(logs.LogDebug).Info("queuing ClusterHealthCheck")
				requests = append(requests, ctrl.Request{
					NamespacedName: client.ObjectKey{
						Name: k.Name,
					},
				})
			}
		}
	}

	return requests
}

func (r *ClusterHealthCheckReconciler) requeueClusterHealthCheckForMachine(
	o client.Object,
) []reconcile.Request {

	machine := o.(*clusterv1.Machine)
	logger := klogr.New().WithValues(
		"objectMapper",
		"requeueClusterHealthCheckForMachine",
		"namespace",
		machine.Namespace,
		"cluster",
		machine.Name,
	)

	addTypeInformationToObject(r.Scheme, machine)

	logger.V(logs.LogDebug).Info("reacting to CAPI Machine change")

	clusterLabelName, ok := machine.Labels[clusterv1.ClusterLabelName]
	if !ok {
		logger.V(logs.LogVerbose).Info("Machine has not ClusterLabelName")
		return nil
	}

	r.Mux.Lock()
	defer r.Mux.Unlock()

	clusterInfo := corev1.ObjectReference{APIVersion: machine.APIVersion, Kind: "Cluster", Namespace: machine.Namespace, Name: clusterLabelName}

	// Get all ClusterHealthCheck previously matching this cluster and reconcile those
	requests := make([]ctrl.Request, r.getClusterMapForEntry(&clusterInfo).Len())
	consumers := r.getClusterMapForEntry(&clusterInfo).Items()

	for i := range consumers {
		requests[i] = ctrl.Request{
			NamespacedName: client.ObjectKey{
				Name: consumers[i].Name,
			},
		}
	}

	// Get Cluster labels
	if clusterLabels, ok := r.ClusterLabels[clusterInfo]; ok {
		// Iterate over all current ClusterHealthCheck and reconcile the ClusterHealthCheck now
		// matching the Cluster
		for k := range r.ClusterHealthChecks {
			clusterHealthCheckSelector := r.ClusterHealthChecks[k]
			parsedSelector, _ := labels.Parse(string(clusterHealthCheckSelector))
			if parsedSelector.Matches(labels.Set(clusterLabels)) {
				l := logger.WithValues("clusterHealthCheck", k.Name)
				l.V(logs.LogDebug).Info("queuing ClusterHealthCheck")
				requests = append(requests, ctrl.Request{
					NamespacedName: client.ObjectKey{
						Name: k.Name,
					},
				})
			}
		}
	}

	return requests
}
