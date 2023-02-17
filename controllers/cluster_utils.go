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

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	libsveltosv1alpha1 "github.com/projectsveltos/libsveltos/api/v1alpha1"
)

// getSveltosCluster returns SveltosCluster
func getSveltosCluster(ctx context.Context, c client.Client,
	clusterNamespace, clusterName string) (*libsveltosv1alpha1.SveltosCluster, error) {

	clusterNamespacedName := types.NamespacedName{
		Namespace: clusterNamespace,
		Name:      clusterName,
	}

	cluster := &libsveltosv1alpha1.SveltosCluster{}
	if err := c.Get(ctx, clusterNamespacedName, cluster); err != nil {
		return nil, err
	}
	return cluster, nil
}

// getCAPICluster returns CAPI Cluster
func getCAPICluster(ctx context.Context, c client.Client,
	clusterNamespace, clusterName string) (*clusterv1.Cluster, error) {

	clusterNamespacedNamed := types.NamespacedName{
		Namespace: clusterNamespace,
		Name:      clusterName,
	}

	cluster := &clusterv1.Cluster{}
	if err := c.Get(ctx, clusterNamespacedNamed, cluster); err != nil {
		return nil, err
	}
	return cluster, nil
}

// isCAPIClusterPaused returns true if CAPI Cluster is paused
func isCAPIClusterPaused(ctx context.Context, c client.Client,
	clusterNamespace, clusterName string) (bool, error) {

	cluster, err := getCAPICluster(ctx, c, clusterNamespace, clusterName)
	if err != nil {
		return false, err
	}

	return cluster.Spec.Paused, nil
}

// isSveltosClusterPaused returns true if CAPI Cluster is paused
func isSveltosClusterPaused(ctx context.Context, c client.Client,
	clusterNamespace, clusterName string) (bool, error) {

	cluster, err := getSveltosCluster(ctx, c, clusterNamespace, clusterName)
	if err != nil {
		return false, err
	}

	return cluster.Spec.Paused, nil
}

func isClusterPaused(ctx context.Context, c client.Client,
	clusterNamespace, clusterName string, clusterType libsveltosv1alpha1.ClusterType) (bool, error) {

	if clusterType == libsveltosv1alpha1.ClusterTypeSveltos {
		return isSveltosClusterPaused(ctx, c, clusterNamespace, clusterName)
	}
	return isCAPIClusterPaused(ctx, c, clusterNamespace, clusterName)
}

func getClusterType(cluster *corev1.ObjectReference) libsveltosv1alpha1.ClusterType {
	// TODO: remove this
	if cluster.APIVersion != libsveltosv1alpha1.GroupVersion.String() &&
		cluster.APIVersion != clusterv1.GroupVersion.String() {

		panic(1)
	}

	clusterType := libsveltosv1alpha1.ClusterTypeCapi
	if cluster.APIVersion == libsveltosv1alpha1.GroupVersion.String() {
		clusterType = libsveltosv1alpha1.ClusterTypeSveltos
	}
	return clusterType
}

func getMatchingCAPIClusters(ctx context.Context, c client.Client, selector labels.Selector,
	logger logr.Logger) ([]corev1.ObjectReference, error) {

	clusterList := &clusterv1.ClusterList{}
	if err := c.List(ctx, clusterList); err != nil {
		logger.Error(err, "failed to list all Cluster")
		return nil, err
	}

	matching := make([]corev1.ObjectReference, 0)

	for i := range clusterList.Items {
		cluster := &clusterList.Items[i]

		if !cluster.DeletionTimestamp.IsZero() {
			// Only existing cluster can match
			continue
		}

		addTypeInformationToObject(c.Scheme(), cluster)
		if selector.Matches(labels.Set(cluster.Labels)) {
			matching = append(matching, corev1.ObjectReference{
				Kind:       cluster.Kind,
				Namespace:  cluster.Namespace,
				Name:       cluster.Name,
				APIVersion: cluster.APIVersion,
			})
		}
	}

	return matching, nil
}

func getMatchingSveltosClusters(ctx context.Context, c client.Client, selector labels.Selector,
	logger logr.Logger) ([]corev1.ObjectReference, error) {

	clusterList := &libsveltosv1alpha1.SveltosClusterList{}
	if err := c.List(ctx, clusterList); err != nil {
		logger.Error(err, "failed to list all Cluster")
		return nil, err
	}

	matching := make([]corev1.ObjectReference, 0)

	for i := range clusterList.Items {
		cluster := &clusterList.Items[i]

		if !cluster.DeletionTimestamp.IsZero() {
			// Only existing cluster can match
			continue
		}

		addTypeInformationToObject(c.Scheme(), cluster)
		if selector.Matches(labels.Set(cluster.Labels)) {
			matching = append(matching, corev1.ObjectReference{
				Kind:       cluster.Kind,
				Namespace:  cluster.Namespace,
				Name:       cluster.Name,
				APIVersion: cluster.APIVersion,
			})
		}
	}

	return matching, nil
}

// getMatchingClusters returns all Sveltos/CAPI Clusters currently matching ClusterHealthCheck.Spec.ClusterSelector
func getMatchingClusters(ctx context.Context, c client.Client, selector labels.Selector,
	logger logr.Logger) ([]corev1.ObjectReference, error) {

	matching := make([]corev1.ObjectReference, 0)

	tmpMatching, err := getMatchingCAPIClusters(ctx, c, selector, logger)
	if err != nil {
		return nil, err
	}

	matching = append(matching, tmpMatching...)

	tmpMatching, err = getMatchingSveltosClusters(ctx, c, selector, logger)
	if err != nil {
		return nil, err
	}

	matching = append(matching, tmpMatching...)

	return matching, nil
}
