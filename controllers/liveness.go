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

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
	"github.com/projectsveltos/libsveltos/lib/clusterproxy"
	logs "github.com/projectsveltos/libsveltos/lib/logsettings"
)

// evaluateLivenessCheck evaluates specific liveness check for a cluster.
// Return values:
// - bool indicating whether liveness check is passing
// - bool indicating if liveness check changed state since last evaluation
// - message is a human consumable information
// - an error if any occurs
func evaluateLivenessCheck(ctx context.Context, c client.Client, clusterNamespace, clusterName string,
	clusterType libsveltosv1beta1.ClusterType, chc *libsveltosv1beta1.ClusterHealthCheck,
	livenessCheck *libsveltosv1beta1.LivenessCheck, logger logr.Logger) (passing, statusChanged bool, message string, err error) {

	logger = logger.WithValues("livenesscheck", fmt.Sprintf("%s:%s", livenessCheck.Type, livenessCheck.Name))
	logger.V(logs.LogDebug).Info("evaluate liveness check type")

	switch livenessCheck.Type {
	case libsveltosv1beta1.LivenessTypeAddons:
		passing, err = evaluateLivenessCheckAddOns(ctx, c, clusterNamespace, clusterName, clusterType,
			chc, livenessCheck, logger)
	case libsveltosv1beta1.LivenessTypeHealthCheck:
		passing, message, err = evaluateLivenessCheckHealthCheck(ctx, c, clusterNamespace, clusterName, clusterType,
			livenessCheck, logger)
	default:
		logger.V(logs.LogInfo).Info("no verification registered for liveness check")
		panic(1)
	}

	if err != nil {
		logger.V(logs.LogInfo).Info("failed to evalute liveness check")
		return
	}

	statusChanged = hasLivenessCheckStatusChange(chc, clusterNamespace, clusterName, clusterType,
		livenessCheck, passing, message)

	return
}

// evaluateLivenessCheckHealthCheck evaluates status reported in corresponding HealthCheckReport.
// Return values:
// - bool indicating if any add-on deployment changed state since last evaluation
// - human consumable message
// - an error if any occurs
func evaluateLivenessCheckHealthCheck(ctx context.Context, c client.Client, clusterNamespace, clusterName string,
	clusterType libsveltosv1beta1.ClusterType, livenessCheck *libsveltosv1beta1.LivenessCheck,
	logger logr.Logger) (allHealthy bool, message string, err error) {

	message = ""
	allHealthy = true
	if livenessCheck.LivenessSourceRef == nil {
		return false, "", nil
	}

	var healthCheckReportList *libsveltosv1beta1.HealthCheckReportList
	healthCheckReportList, err = fetchHealthCheckReports(ctx, c, clusterNamespace,
		clusterName, livenessCheck.LivenessSourceRef.Name, clusterType)
	if err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to fetch healthCheckReports: %v", err))
		return false, "", err
	}

	if len(healthCheckReportList.Items) == 0 {
		logger.V(logs.LogInfo).Info("did not find healthCheckReport")
		return false, "", err
	}

	for i := range healthCheckReportList.Items {
		hcr := &healthCheckReportList.Items[i]
		if hcr.DeletionTimestamp.IsZero() {
			msg, isHealthy := isStatusHealthy(hcr)
			if !isHealthy {
				allHealthy = false
			}
			message += msg
		}
	}

	return allHealthy, message, nil
}

// evaluateLivenessCheckAddOns evaluates whether all add-ons are deployed or not.
// Return values:
// - bool indicating if any add-on deployment changed state since last evaluation
// - an error if any occurs
func evaluateLivenessCheckAddOns(ctx context.Context, c client.Client, clusterNamespace, clusterName string,
	clusterType libsveltosv1beta1.ClusterType, chc *libsveltosv1beta1.ClusterHealthCheck,
	livenessCheck *libsveltosv1beta1.LivenessCheck, logger logr.Logger) (bool, error) {

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
func hasLivenessCheckStatusChange(chc *libsveltosv1beta1.ClusterHealthCheck, clusterNamespace, clusterName string,
	clusterType libsveltosv1beta1.ClusterType, livenessCheck *libsveltosv1beta1.LivenessCheck,
	passing bool, message string) bool {

	for i := range chc.Status.ClusterConditions {
		cc := &chc.Status.ClusterConditions[i]
		if isClusterConditionForCluster(cc, clusterNamespace, clusterName, clusterType) {
			previousStatus := getLivenessCheckStatus(cc, livenessCheck)
			if previousStatus == nil {
				// No previous status found
				return true
			}

			return hasStatusChanged(previousStatus, passing, message)
		}
	}

	// No previous status found
	return true
}

func hasStatusChanged(previousStatus *libsveltosv1beta1.Condition, passing bool, message string) bool {
	// if currently liveness check is passing
	if passing {
		// No change only if previous status was also conditionTrue
		return previousStatus.Status != corev1.ConditionTrue
	}

	// No change only if previous status was conditionFalse
	return previousStatus.Message != message ||
		previousStatus.Status != corev1.ConditionFalse
}

// getLivenessCheckStatus returns the liveness check status if ever set before. Nil otherwise
func getLivenessCheckStatus(cc *libsveltosv1beta1.ClusterCondition, livenessCheck *libsveltosv1beta1.LivenessCheck,
) *libsveltosv1beta1.Condition {

	livenessCheckType := getConditionType(livenessCheck)

	for i := range cc.Conditions {
		condition := cc.Conditions[i]
		if condition.Type == libsveltosv1beta1.ConditionType(livenessCheckType) &&
			condition.Name == livenessCheck.Name {

			return &condition
		}
	}

	return nil
}

// isClusterConditionForCluster returns true if the ClusterCondition is for the cluster clusterType, clusterNamespace,
// clusterName
func isClusterConditionForCluster(cc *libsveltosv1beta1.ClusterCondition, clusterNamespace, clusterName string,
	clusterType libsveltosv1beta1.ClusterType) bool {

	return cc.ClusterInfo.Cluster.Namespace == clusterNamespace &&
		cc.ClusterInfo.Cluster.Name == clusterName &&
		clusterproxy.GetClusterType(&cc.ClusterInfo.Cluster) == clusterType
}

// fetchClusterSummaries returns all ClusterSummaries currently existing for a given cluster
func fetchClusterSummaries(ctx context.Context, c client.Client, clusterNamespace, clusterName string,
	clusterType libsveltosv1beta1.ClusterType) (*configv1beta1.ClusterSummaryList, error) {

	// Fecth all ClusterSummary for this Cluster
	listOptions := []client.ListOption{
		client.InNamespace(clusterNamespace),
		client.MatchingLabels{
			configv1beta1.ClusterNameLabel: clusterName,
			configv1beta1.ClusterTypeLabel: string(clusterType),
		},
	}

	clusterSummaryList := &configv1beta1.ClusterSummaryList{}
	err := c.List(ctx, clusterSummaryList, listOptions...)
	return clusterSummaryList, err
}

// areAddonsDeployed returns whether all add-ons referenced by a ClusterSummary instance are deployed
// or not.
func areAddonsDeployed(clusterSummary *configv1beta1.ClusterSummary) bool {
	for i := range clusterSummary.Status.FeatureSummaries {
		fs := clusterSummary.Status.FeatureSummaries[i]
		if fs.Status != libsveltosv1beta1.FeatureStatus(libsveltosv1beta1.SveltosStatusProvisioned) {
			return false
		}
	}

	return true
}

// isStatusHealthy returns whether state is Healthy.
func isStatusHealthy(hcr *libsveltosv1beta1.HealthCheckReport) (string, bool) {
	var message string
	isAllHealthy := true

	for i := range hcr.Spec.ResourceStatuses {
		rs := hcr.Spec.ResourceStatuses[i]
		if rs.HealthStatus != libsveltosv1beta1.HealthStatusHealthy {
			isAllHealthy = false
			message += fmt.Sprintf("%s: %s/%s status is %s  \n",
				rs.ObjectRef.Kind, rs.ObjectRef.Namespace, rs.ObjectRef.Name, rs.HealthStatus)
			if rs.Message != "" {
				message += fmt.Sprintf("Message: %s  \n", rs.Message)
			}
		}
	}

	return message, isAllHealthy
}

// fetchHealthCheckReports returns healthCheckReports for given HealthCheck in a given cluster
func fetchHealthCheckReports(ctx context.Context, c client.Client, clusterNamespace, clusterName, healthCheckName string,
	clusterType libsveltosv1beta1.ClusterType) (*libsveltosv1beta1.HealthCheckReportList, error) {

	labels := libsveltosv1beta1.GetHealthCheckReportLabels(healthCheckName, clusterName, &clusterType)

	// Fecth all ClusterSummary for this Cluster
	listOptions := []client.ListOption{
		client.InNamespace(clusterNamespace),
		client.MatchingLabels(labels),
	}

	healthCheckReportList := &libsveltosv1beta1.HealthCheckReportList{}
	err := c.List(ctx, healthCheckReportList, listOptions...)
	return healthCheckReportList, err
}

func getConditionType(livenessCheck *libsveltosv1beta1.LivenessCheck) string {
	return fmt.Sprintf("%s:%s", string(livenessCheck.Type), livenessCheck.Name)
}

func getConditionStatus(passing bool) corev1.ConditionStatus {
	if passing {
		return corev1.ConditionTrue
	}

	return corev1.ConditionFalse
}
