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

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	libsveltosv1alpha1 "github.com/projectsveltos/libsveltos/api/v1alpha1"
	logs "github.com/projectsveltos/libsveltos/lib/logsettings"
	configv1alpha1 "github.com/projectsveltos/sveltos-manager/api/v1alpha1"
)

// evaluateLivenessCheck evaluates specific liveness check for a cluster.
// Return values:
// - bool indicating whether liveness check is passing
// - bool indicating if liveness check changed state since last evaluation
// - an error if any occurs
func evaluateLivenessCheck(ctx context.Context, c client.Client, clusterNamespace, clusterName string,
	clusterType libsveltosv1alpha1.ClusterType, chc *libsveltosv1alpha1.ClusterHealthCheck,
	livenessCheck *libsveltosv1alpha1.LivenessCheck, logger logr.Logger) (passing, statusChanged bool, err error) {

	logger = logger.WithValues("livenesscheck", fmt.Sprintf("%s:%s", livenessCheck.Type, livenessCheck.Name))
	logger.V(logs.LogDebug).Info("evaluate liveness check type")

	switch livenessCheck.Type {
	case libsveltosv1alpha1.LivenessTypeAddons:
		passing, err = evaluateLivenessCheckAddOns(ctx, c, clusterNamespace, clusterName, clusterType,
			chc, livenessCheck, logger)
	default:
		logger.V(logs.LogInfo).Info("no verification registered for liveness check")
		panic(1)
	}

	if err != nil {
		logger.V(logs.LogInfo).Info("failed to evalute liveness check")
		return
	}

	statusChanged = hasLivenessCheckStatusChange(chc, clusterNamespace, clusterName, clusterType, livenessCheck, passing)

	return
}

// evaluateLivenessCheckAddOns evaluates whether all add-ons are deployed or not.
// Return values:
// - bool indicating if any add-on deployment changed state since last evaluation
// - an error if any occurs
func evaluateLivenessCheckAddOns(ctx context.Context, c client.Client, clusterNamespace, clusterName string,
	clusterType libsveltosv1alpha1.ClusterType, chc *libsveltosv1alpha1.ClusterHealthCheck,
	livenessCheck *libsveltosv1alpha1.LivenessCheck, logger logr.Logger) (bool, error) {

	clusterSummaries, err := fetchClusterSummaries(ctx, c, clusterNamespace, clusterName, clusterType)
	if err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to fetch clustersummmaries: %v", err))
		return false, err
	}

	deployed := true
	for i := range clusterSummaries.Items {
		cs := &clusterSummaries.Items[i]
		if cs.DeletionTimestamp.IsZero() && !areAddonsDeployed(&clusterSummaries.Items[i]) {
			deployed = false
		}
	}

	return deployed, nil
}

// hasLivenessCheckStatusChange returns true if the status for this liveness check has changed since last evaluation
func hasLivenessCheckStatusChange(chc *libsveltosv1alpha1.ClusterHealthCheck, clusterNamespace, clusterName string,
	clusterType libsveltosv1alpha1.ClusterType, livenessCheck *libsveltosv1alpha1.LivenessCheck, passing bool) bool {

	for i := range chc.Status.ClusterConditions {
		cc := &chc.Status.ClusterConditions[i]
		if isClusterConditionForCluster(cc, clusterNamespace, clusterName, clusterType) {
			previousStatus := getLivenessCheckStatus(cc, livenessCheck)
			if previousStatus == nil {
				// No previous status found
				return true
			}

			return hasStatusChanged(previousStatus, passing)
		}
	}

	// No previous status found
	return true
}

func hasStatusChanged(previousStatus *corev1.ConditionStatus, passing bool) bool {
	// if currently liveness check is passing
	if passing {
		// No change only if previous status was conditionTrue
		return *previousStatus != corev1.ConditionTrue
	}

	// No change only if previous status was conditionFalse
	return *previousStatus != corev1.ConditionFalse
}

// getLivenessCheckStatus returns the liveness check status if ever set before. Nil otherwise
func getLivenessCheckStatus(cc *libsveltosv1alpha1.ClusterCondition, livenessCheck *libsveltosv1alpha1.LivenessCheck,
) *corev1.ConditionStatus {

	livenessCheckType := getConditionType(livenessCheck)

	for i := range cc.Conditions {
		condition := cc.Conditions[i]
		if condition.Type == libsveltosv1alpha1.ConditionType(livenessCheckType) {
			return &condition.Status
		}
	}

	return nil
}

// isClusterConditionForCluster returns true if the ClusterCondition is for the cluster clusterType, clusterNamespace,
// clusterName
func isClusterConditionForCluster(cc *libsveltosv1alpha1.ClusterCondition, clusterNamespace, clusterName string,
	clusterType libsveltosv1alpha1.ClusterType) bool {

	return cc.ClusterInfo.Cluster.Namespace == clusterNamespace &&
		cc.ClusterInfo.Cluster.Name == clusterName &&
		getClusterType(&cc.ClusterInfo.Cluster) == clusterType
}

// fetchClusterSummaries returns all ClusterSummaries currently existing for a given cluster
func fetchClusterSummaries(ctx context.Context, c client.Client, clusterNamespace, clusterName string,
	clusterType libsveltosv1alpha1.ClusterType) (*configv1alpha1.ClusterSummaryList, error) {
	// Fecth all ClusterSummary for this Cluster
	listOptions := []client.ListOption{
		client.InNamespace(clusterNamespace),
		client.MatchingLabels{
			configv1alpha1.ClusterNameLabel: clusterName,
			configv1alpha1.ClusterTypeLabel: string(clusterType),
		},
	}

	clusterSummaryList := &configv1alpha1.ClusterSummaryList{}
	err := c.List(ctx, clusterSummaryList, listOptions...)
	return clusterSummaryList, err
}

// areAddonsDeployed returns whether all add-ons referenced by a ClusterSummary instance are deployed
// or not.
func areAddonsDeployed(clusterSummary *configv1alpha1.ClusterSummary) bool {
	for i := range clusterSummary.Status.FeatureSummaries {
		fs := clusterSummary.Status.FeatureSummaries[i]
		if fs.Status != configv1alpha1.FeatureStatus(libsveltosv1alpha1.SveltosStatusProvisioned) {
			return false
		}
	}

	return true
}

func getConditionType(livenessCheck *libsveltosv1alpha1.LivenessCheck) string {
	return fmt.Sprintf("%s:%s", string(livenessCheck.Type), livenessCheck.Name)
}

func getConditionStatus(passing bool) corev1.ConditionStatus {
	if passing {
		return corev1.ConditionTrue
	}

	return corev1.ConditionFalse
}
