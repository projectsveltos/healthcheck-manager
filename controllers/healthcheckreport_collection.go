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
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
	"github.com/projectsveltos/libsveltos/lib/clusterproxy"
	logs "github.com/projectsveltos/libsveltos/lib/logsettings"
	"github.com/projectsveltos/libsveltos/lib/sveltos_upgrade"
)

const (
	malformedLabelError = "healthCheckReport is malformed. Labels is empty"
	missingLabelError   = "healthCheckReport is malformed. Label missing"
)

// removeHealthCheckReports deletes all HealthCheckReport corresponding to HealthCheck instance
func removeHealthCheckReports(ctx context.Context, c client.Client, healthCheck *libsveltosv1beta1.HealthCheck,
	logger logr.Logger) error {

	listOptions := []client.ListOption{
		client.MatchingLabels{
			libsveltosv1beta1.HealthCheckNameLabel: healthCheck.Name,
		},
	}

	healthCheckReportList := &libsveltosv1beta1.HealthCheckReportList{}
	err := c.List(ctx, healthCheckReportList, listOptions...)
	if err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to list HealthCheckReports. Err: %v", err))
		return err
	}

	for i := range healthCheckReportList.Items {
		cr := &healthCheckReportList.Items[i]
		err = c.Delete(ctx, cr)
		if err != nil {
			return err
		}
	}

	return nil
}

// removeHealthCheckReportsFromCluster deletes all HealthCheckReport corresponding to Cluster instance
func removeHealthCheckReportsFromCluster(ctx context.Context, c client.Client, clusterNamespace, clusterName string,
	clusterType libsveltosv1beta1.ClusterType, logger logr.Logger) error {

	listOptions := []client.ListOption{
		client.MatchingLabels{
			libsveltosv1beta1.HealthCheckReportClusterNameLabel: clusterName,
			libsveltosv1beta1.HealthCheckReportClusterTypeLabel: strings.ToLower(string(clusterType)),
		},
	}

	healthCheckReportList := &libsveltosv1beta1.HealthCheckReportList{}
	err := c.List(ctx, healthCheckReportList, listOptions...)
	if err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to list HealthCheckReports. Err: %v", err))
		return err
	}

	for i := range healthCheckReportList.Items {
		cr := &healthCheckReportList.Items[i]
		err = c.Delete(ctx, cr)
		if err != nil {
			return err
		}
	}

	return nil
}

// Periodically collects HealthCheckReports from each managed cluster.
func collectHealthCheckReports(c client.Client, shardKey, capiOnboardAnnotation, version string, logger logr.Logger) {
	interval := 10 * time.Second
	if shardKey != "" {
		// This controller will only fetch ClassifierReport instances
		// so it can be more aggressive
		interval = 5 * time.Second
	}

	ctx := context.TODO()
	for {
		logger.V(logs.LogDebug).Info("collecting HealthCheckReports")
		clusterList, err := clusterproxy.GetListOfClustersForShardKey(ctx, c, "", capiOnboardAnnotation, shardKey, logger)
		if err != nil {
			logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to get clusters: %v", err))
		}

		for i := range clusterList {
			cluster := &clusterList[i]
			err = collectAndProcessHealthCheckReportsFromCluster(ctx, c, cluster, version, logger)
			if err != nil {
				logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to collect HealthCheckReports from cluster: %s %s/%s %v",
					cluster.Kind, cluster.Namespace, cluster.Name, err))
			}
		}

		time.Sleep(interval)
	}
}

func collectAndProcessHealthCheckReportsFromCluster(ctx context.Context, c client.Client,
	cluster *corev1.ObjectReference, version string, logger logr.Logger) error {

	logger = logger.WithValues("cluster", fmt.Sprintf("%s/%s", cluster.Namespace, cluster.Name))
	clusterRef := &corev1.ObjectReference{
		Namespace:  cluster.Namespace,
		Name:       cluster.Name,
		APIVersion: cluster.APIVersion,
		Kind:       cluster.Kind,
	}
	ready, err := clusterproxy.IsClusterReadyToBeConfigured(ctx, c, clusterRef, logger)
	if err != nil {
		logger.V(logs.LogDebug).Info("cluster is not ready yet")
		return err
	}

	if !ready {
		return nil
	}

	if !sveltos_upgrade.IsSveltosAgentVersionCompatible(ctx, c, version, cluster.Namespace, cluster.Name,
		clusterproxy.GetClusterType(clusterRef), getAgentInMgmtCluster(), logger) {

		logger.V(logs.LogDebug).Info(compatibilityErrorMsg)
		return errors.New(compatibilityErrorMsg)
	}

	logger.V(logs.LogDebug).Info("collecting HealthCheckReports from cluster")

	// EventReports location depends on sveltos-agent: management cluster if it's running there,
	// otherwise managed cluster.
	clusterClient, err := getHealthCheckReportClient(ctx, cluster.Namespace, cluster.Name,
		clusterproxy.GetClusterType(clusterRef), logger)
	if err != nil {
		return err
	}

	var listOptions []client.ListOption
	if getAgentInMgmtCluster() {
		// If agent is in the management cluster, EventReports for this cluster are also
		// in the management cluuster in the cluster namespace.
		listOptions = []client.ListOption{
			client.InNamespace(cluster.Namespace),
		}
	}

	healthCheckReportList := libsveltosv1beta1.HealthCheckReportList{}
	err = clusterClient.List(ctx, &healthCheckReportList, listOptions...)
	if err != nil {
		return err
	}

	for i := range healthCheckReportList.Items {
		hcr := &healthCheckReportList.Items[i]

		if shouldIgnore(hcr) {
			continue
		}

		l := logger.WithValues("healthCheckReport", hcr.Name)
		var mgmtClusterHealthCheckReport *libsveltosv1beta1.HealthCheckReport
		// First update/delete healthCheckReports in managemnent cluster
		if !hcr.DeletionTimestamp.IsZero() {
			logger.V(logs.LogDebug).Info("deleting from management cluster")
			err = deleteHealthCheckReport(ctx, c, cluster, hcr, l)
			if err != nil {
				logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to delete HealthCheckReport in management cluster. Err: %v", err))
			}
		} else {
			logger.V(logs.LogDebug).Info("updating in management cluster")
			mgmtClusterHealthCheckReport, err = updateHealthCheckReport(ctx, c, cluster, hcr, l)
			if err != nil {
				logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to update HealthCheckReport in management cluster. Err: %v", err))
			}
		}

		if getAgentInMgmtCluster() {
			if mgmtClusterHealthCheckReport != nil {
				// If in agentless mode, the Status of EventReport in the management cluster will be updated.
				// So set er to current version (update otherwise will fail with object has been modified)
				hcr = mgmtClusterHealthCheckReport
			}
		}

		logger.V(logs.LogDebug).Info("updating in managed cluster")
		// Update HealthCheckReport Status in managed cluster
		phase := libsveltosv1beta1.ReportProcessed
		hcr.Status.Phase = &phase
		err = clusterClient.Status().Update(ctx, hcr)
		if err != nil {
			logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to update HealthCheckReport in managed cluster. Err: %v", err))
		}
	}

	return nil
}

func deleteHealthCheckReport(ctx context.Context, c client.Client, cluster *corev1.ObjectReference,
	healthCheckReport *libsveltosv1beta1.HealthCheckReport, logger logr.Logger) error {

	if healthCheckReport.Labels == nil {
		logger.V(logs.LogInfo).Info(malformedLabelError)
		return errors.New(malformedLabelError)
	}

	healthCheckName, ok := healthCheckReport.Labels[libsveltosv1beta1.HealthCheckNameLabel]
	if !ok {
		logger.V(logs.LogInfo).Info(missingLabelError)
		return errors.New(missingLabelError)
	}

	clusterType := clusterproxy.GetClusterType(cluster)
	healthCheckReportName := libsveltosv1beta1.GetHealthCheckReportName(healthCheckName, cluster.Name, &clusterType)

	currentHealthCheckReport := &libsveltosv1beta1.HealthCheckReport{}
	err := c.Get(ctx,
		types.NamespacedName{Namespace: cluster.Namespace, Name: healthCheckReportName},
		currentHealthCheckReport)
	if err == nil {
		return c.Delete(ctx, currentHealthCheckReport)
	}

	return nil
}

func updateHealthCheckReport(ctx context.Context, c client.Client, cluster *corev1.ObjectReference,
	healthCheckReport *libsveltosv1beta1.HealthCheckReport, logger logr.Logger) (*libsveltosv1beta1.HealthCheckReport, error) {

	if healthCheckReport.Spec.ClusterName != "" {
		// if ClusterName is set, this is coming from a
		// managed cluster. If management cluster is in turn
		// managed by another cluster, do not pull those.
		return healthCheckReport, nil
	}

	if healthCheckReport.Labels == nil {
		logger.V(logs.LogInfo).Info(malformedLabelError)
		return healthCheckReport, errors.New(malformedLabelError)
	}

	healthCheckName, ok := healthCheckReport.Labels[libsveltosv1beta1.HealthCheckNameLabel]
	if !ok {
		logger.V(logs.LogInfo).Info(missingLabelError)
		return healthCheckReport, errors.New(missingLabelError)
	}

	// Verify HealthCheck still exists
	currentHealthCheck := libsveltosv1beta1.HealthCheck{}
	err := c.Get(ctx, types.NamespacedName{Name: healthCheckName}, &currentHealthCheck)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return healthCheckReport, nil
		}
	}
	if !currentHealthCheck.DeletionTimestamp.IsZero() {
		return healthCheckReport, nil
	}

	clusterType := clusterproxy.GetClusterType(cluster)
	healthCheckReportName := libsveltosv1beta1.GetHealthCheckReportName(healthCheckName, cluster.Name, &clusterType)

	currentHealthCheckReport := &libsveltosv1beta1.HealthCheckReport{}
	err = c.Get(ctx,
		types.NamespacedName{Namespace: cluster.Namespace, Name: healthCheckReportName},
		currentHealthCheckReport)
	if err != nil {
		if apierrors.IsNotFound(err) {
			logger.V(logs.LogDebug).Info("create HealthCheckReport in management cluster")
			currentHealthCheckReport.Namespace = cluster.Namespace
			currentHealthCheckReport.Name = healthCheckReportName
			currentHealthCheckReport.Labels = libsveltosv1beta1.GetHealthCheckReportLabels(
				healthCheckName, cluster.Name, &clusterType)
			currentHealthCheckReport.Spec = healthCheckReport.Spec
			currentHealthCheckReport.Spec.ClusterNamespace = cluster.Namespace
			currentHealthCheckReport.Spec.ClusterName = cluster.Name
			currentHealthCheckReport.Spec.ClusterType = clusterType
			err = c.Create(ctx, currentHealthCheckReport)
			if err == nil {
				return currentHealthCheckReport, nil
			}
		}
		return healthCheckReport, err
	}

	logger.V(logs.LogDebug).Info("update HealthCheckReport in management cluster")
	currentHealthCheckReport.Spec = healthCheckReport.Spec
	currentHealthCheckReport.Spec.ClusterNamespace = cluster.Namespace
	currentHealthCheckReport.Spec.ClusterName = cluster.Name
	currentHealthCheckReport.Spec.ClusterType = clusterType
	currentHealthCheckReport.Labels = libsveltosv1beta1.GetHealthCheckReportLabels(
		healthCheckName, cluster.Name, &clusterType)
	err = c.Update(ctx, currentHealthCheckReport)
	if err == nil {
		return currentHealthCheckReport, nil
	}
	return healthCheckReport, err
}

// HealthCheckReports are collected from managed cluster to the management cluster.
// When an HealthCheckReport is collected from a managed cluster and created in the
// management cluster, the label healthcheckreport.projectsveltos.io/cluster-name
// is added. All HealthCheckReport found in the management cluster with this
// labels should be ignored as collected from other managed clusters.
func shouldIgnore(er *libsveltosv1beta1.HealthCheckReport) bool {
	if getAgentInMgmtCluster() {
		// If sveltos-agent is in the management cluster, HealthCheckReports
		// are directly generated by sveltos-agent here. So there is no
		// copy to ignore.
		return false
	}

	if er.Labels == nil {
		return false
	}

	_, ok := er.Labels[libsveltosv1beta1.HealthCheckReportClusterNameLabel]
	return ok
}
