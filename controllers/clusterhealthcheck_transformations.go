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

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/klog/v2/textlogger"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	configv1alpha1 "github.com/projectsveltos/addon-controller/api/v1alpha1"
	libsveltosv1alpha1 "github.com/projectsveltos/libsveltos/api/v1alpha1"
	logs "github.com/projectsveltos/libsveltos/lib/logsettings"
)

func (r *ClusterHealthCheckReconciler) requeueClusterHealthCheckForHealthCheckReport(
	ctx context.Context, o client.Object,
) []reconcile.Request {

	healthCheckReport := o.(*libsveltosv1alpha1.HealthCheckReport)
	logger := textlogger.NewLogger(textlogger.NewConfig(textlogger.Verbosity(1))).WithValues(
		"healthCheckReport", fmt.Sprintf("%s/%s", healthCheckReport.GetNamespace(), healthCheckReport.GetName()))

	logger.V(logs.LogDebug).Info("reacting to healthCheckReport change")

	r.Mux.Lock()
	defer r.Mux.Unlock()

	// Use the HealthCheck this HealthCheckReport is about
	healthCheckInfo := corev1.ObjectReference{APIVersion: libsveltosv1alpha1.GroupVersion.String(),
		Kind: libsveltosv1alpha1.HealthCheckKind, Name: healthCheckReport.Spec.HealthCheckName}

	// Get all ClusterHealthChecks referencing this HealthCheck
	requests := make([]ctrl.Request, r.getReferenceMapForEntry(&healthCheckInfo).Len())
	consumers := r.getReferenceMapForEntry(&healthCheckInfo).Items()

	for i := range consumers {
		l := logger.WithValues("clusterHealthCheck", consumers[i].Name)
		l.V(logs.LogDebug).Info("queuing ClusterHealthCheck")
		requests[i] = ctrl.Request{
			NamespacedName: client.ObjectKey{
				Name: consumers[i].Name,
			},
		}
	}

	return requests
}

func (r *ClusterHealthCheckReconciler) requeueClusterHealthCheckForHealthCheck(
	ctx context.Context, o client.Object,
) []reconcile.Request {

	healthCheck := o.(*libsveltosv1alpha1.HealthCheck)
	logger := textlogger.NewLogger(textlogger.NewConfig(textlogger.Verbosity(1))).WithValues(
		"healthCheck", healthCheck.GetName())

	logger.V(logs.LogDebug).Info("reacting to healthCheck change")

	r.Mux.Lock()
	defer r.Mux.Unlock()

	healthCheckInfo := corev1.ObjectReference{APIVersion: libsveltosv1alpha1.GroupVersion.String(),
		Kind: libsveltosv1alpha1.HealthCheckKind, Name: healthCheck.Name}

	// Get all ClusterHealthChecks referencing this HealthCheck
	requests := make([]ctrl.Request, r.getReferenceMapForEntry(&healthCheckInfo).Len())
	consumers := r.getReferenceMapForEntry(&healthCheckInfo).Items()

	for i := range consumers {
		l := logger.WithValues("clusterHealthCheck", consumers[i].Name)
		l.V(logs.LogDebug).Info("queuing ClusterHealthCheck")
		requests[i] = ctrl.Request{
			NamespacedName: client.ObjectKey{
				Name: consumers[i].Name,
			},
		}
	}

	return requests
}

func (r *ClusterHealthCheckReconciler) requeueClusterHealthCheckForSveltosCluster(
	ctx context.Context, o client.Object,
) []reconcile.Request {

	return r.requeueClusterHealthCheckForACluster(o)
}

func (r *ClusterHealthCheckReconciler) requeueClusterHealthCheckForCluster(
	ctx context.Context, cluster *clusterv1.Cluster,
) []reconcile.Request {

	return r.requeueClusterHealthCheckForACluster(cluster)
}

func (r *ClusterHealthCheckReconciler) requeueClusterHealthCheckForACluster(
	cluster client.Object,
) []reconcile.Request {

	logger := textlogger.NewLogger(textlogger.NewConfig(textlogger.Verbosity(1))).WithValues(
		"cluster", fmt.Sprintf("%s/%s", cluster.GetNamespace(), cluster.GetName()))

	logger.V(logs.LogDebug).Info("reacting to Cluster change")

	r.Mux.Lock()
	defer r.Mux.Unlock()

	addTypeInformationToObject(r.Scheme, cluster)

	apiVersion, kind := cluster.GetObjectKind().GroupVersionKind().ToAPIVersionAndKind()

	clusterInfo := corev1.ObjectReference{APIVersion: apiVersion, Kind: kind,
		Namespace: cluster.GetNamespace(), Name: cluster.GetName()}

	r.ClusterLabels[clusterInfo] = cluster.GetLabels()

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
		parsedSelector, err := labels.Parse(string(clusterHealthCheckSelector))
		if err != nil {
			// When clusterSelector is fixed, this ClusterHealthCheck instance
			// will be reconciled
			continue
		}
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
	ctx context.Context, o client.Object,
) []reconcile.Request {

	clusterSummary := o
	logger := textlogger.NewLogger(textlogger.NewConfig(textlogger.Verbosity(1))).WithValues(
		"clusterSummary", fmt.Sprintf("%s/%s", clusterSummary.GetNamespace(), clusterSummary.GetName()))

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
			parsedSelector, err := labels.Parse(string(clusterHealthCheckSelector))
			if err != nil {
				// When clusterSelector is fixed, this ClusterHealthCheck instance
				// will be reconciled
				continue
			}
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
	ctx context.Context, machine *clusterv1.Machine,
) []reconcile.Request {

	logger := textlogger.NewLogger(textlogger.NewConfig(textlogger.Verbosity(1))).WithValues(
		"machine", fmt.Sprintf("%s/%s", machine.GetNamespace(), machine.GetName()))

	addTypeInformationToObject(r.Scheme, machine)

	logger.V(logs.LogDebug).Info("reacting to CAPI Machine change")

	ClusterNameLabel, ok := machine.Labels[clusterv1.ClusterNameLabel]
	if !ok {
		logger.V(logs.LogVerbose).Info("Machine has not ClusterNameLabel")
		return nil
	}

	r.Mux.Lock()
	defer r.Mux.Unlock()

	clusterInfo := corev1.ObjectReference{APIVersion: machine.APIVersion, Kind: "Cluster", Namespace: machine.Namespace, Name: ClusterNameLabel}

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
			parsedSelector, err := labels.Parse(string(clusterHealthCheckSelector))
			if err != nil {
				// When clusterSelector is fixed, this ClusterHealthCheck instance
				// will be reconciled
				continue
			}
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
