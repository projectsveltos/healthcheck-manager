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
	"sync"
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

	// clustersPerWorker controls how many clusters each worker goroutine handles.
	clustersPerWorker = 50
	// maxCollectionWorkers caps the number of concurrent goroutines collecting HealthCheckReports.
	maxCollectionWorkers = 5
)

// clusterKey uniquely identifies a cluster by namespace, name, and type so that a
// SveltosCluster and a CAPI Cluster sharing the same namespace and name are treated separately.
type clusterKey struct{ ns, name, clusterType string }

// buildShardClustersMap returns a lookup of shard-local clusters keyed by clusterKey.
func buildShardClustersMap(clusterList []corev1.ObjectReference) map[clusterKey]corev1.ObjectReference {
	m := make(map[clusterKey]corev1.ObjectReference, len(clusterList))
	for i := range clusterList {
		ref := clusterList[i]
		ct := strings.ToLower(string(clusterproxy.GetClusterType(&ref)))
		m[clusterKey{ref.Namespace, ref.Name, ct}] = ref
	}
	return m
}

// findClusterForReport extracts the cluster key from a report's labels and checks whether
// that cluster is in shardClusters. Returns (key, ref, true) on a match.
func findClusterForReport(ns string, labels map[string]string,
	clusterNameLabel, clusterTypeLabel string,
	shardClusters map[clusterKey]corev1.ObjectReference,
) (clusterKey, corev1.ObjectReference, bool) {

	if labels == nil {
		return clusterKey{}, corev1.ObjectReference{}, false
	}
	clusterName := labels[clusterNameLabel]
	clusterType := labels[clusterTypeLabel]
	if clusterName == "" || clusterType == "" {
		return clusterKey{}, corev1.ObjectReference{}, false
	}
	key := clusterKey{ns, clusterName, clusterType}
	ref, ok := shardClusters[key]
	return key, ref, ok
}

// clusterReportGroup holds a cluster reference and all report items belonging to it.
type clusterReportGroup[T any] struct {
	ref   corev1.ObjectReference
	items []*T
}

// groupReportsByCluster groups report items by the cluster identified via clusterNameLabel and
// clusterTypeLabel, filtering to only shard-local clusters.
func groupReportsByCluster[T any](
	reports []T,
	getLabels func(*T) map[string]string,
	getNamespace func(*T) string,
	clusterNameLabel, clusterTypeLabel string,
	shardClusters map[clusterKey]corev1.ObjectReference,
) []*clusterReportGroup[T] {

	byCluster := make(map[clusterKey]*clusterReportGroup[T])
	for i := range reports {
		item := &reports[i]
		key, ref, ok := findClusterForReport(getNamespace(item), getLabels(item),
			clusterNameLabel, clusterTypeLabel, shardClusters)
		if !ok {
			continue
		}
		cd, exists := byCluster[key]
		if !exists {
			cd = &clusterReportGroup[T]{ref: ref}
			byCluster[key] = cd
		}
		cd.items = append(cd.items, item)
	}

	groups := make([]*clusterReportGroup[T], 0, len(byCluster))
	for _, cd := range byCluster {
		groups = append(groups, cd)
	}
	return groups
}

// removeHealthCheckReports deletes all HealthCheckReport corresponding to HealthCheck instance
func removeHealthCheckReports(ctx context.Context, c client.Client, healthCheckName string,
	logger logr.Logger) error {

	listOptions := []client.ListOption{
		client.MatchingLabels{
			libsveltosv1beta1.HealthCheckNameLabel: healthCheckName,
		},
	}

	healthCheckReportList := &libsveltosv1beta1.HealthCheckReportList{}
	err := c.List(ctx, healthCheckReportList, listOptions...)
	if err != nil {
		logger.V(logs.LogInfo).Error(err, "failed to list HealthCheckReports")
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
		logger.V(logs.LogInfo).Error(err, "failed to list HealthCheckReports")
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

// buildClustersWithHealthCheck returns the set of all clusters currently matched
// by at least one ClusterHealthCheck, derived from ClusterHealthCheck.Status.MatchingClusterRefs.
// This is used to skip clusters that have no ClusterHealthChecks and therefore no
// HealthCheckReports to collect.
func buildClustersWithHealthCheck(clusterHealthChecks *libsveltosv1beta1.ClusterHealthCheckList) map[corev1.ObjectReference]bool {
	clusters := make(map[corev1.ObjectReference]bool)
	for i := range clusterHealthChecks.Items {
		for j := range clusterHealthChecks.Items[i].Status.MatchingClusterRefs {
			clusters[clusterHealthChecks.Items[i].Status.MatchingClusterRefs[j]] = true
		}
	}
	return clusters
}

// collectHealthCheckReportFromCluster delegates per-cluster collection to
// collectAndProcessHealthCheckReportsFromCluster with a cluster-tagged logger.
func collectHealthCheckReportFromCluster(ctx context.Context, c client.Client,
	cluster *corev1.ObjectReference, version string, logger logr.Logger) error {

	l := logger.WithValues("cluster", fmt.Sprintf("%s %s/%s", cluster.Kind, cluster.Namespace, cluster.Name))
	return collectAndProcessHealthCheckReportsFromCluster(ctx, c, cluster, version, l)
}

// processHealthCheckReportClusters collects and processes HealthCheckReports for the given clusters.
// It runs sequentially when there are fewer than clustersPerWorker clusters, and spawns
// up to maxCollectionWorkers goroutines otherwise.
func processHealthCheckReportClusters(ctx context.Context, c client.Client,
	clusters []corev1.ObjectReference, version string, logger logr.Logger) error {

	logErr := func(cluster *corev1.ObjectReference, err error) {
		logger.WithValues("cluster", fmt.Sprintf("%s/%s", cluster.Namespace, cluster.Name)).
			V(logs.LogInfo).Info(fmt.Sprintf("failed to collect HealthCheckReports: %v", err))
	}

	numWorkers := len(clusters) / clustersPerWorker
	if numWorkers > maxCollectionWorkers {
		numWorkers = maxCollectionWorkers
	}

	if numWorkers == 0 {
		// Sequential: fewer than clustersPerWorker clusters, goroutine overhead not justified.
		var retErr error
		for i := range clusters {
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}
			cluster := &clusters[i]
			if err := collectHealthCheckReportFromCluster(ctx, c, cluster, version, logger); err != nil {
				logErr(cluster, err)
				retErr = err
			}
		}
		return retErr
	}

	// Parallel: one worker per clustersPerWorker clusters, capped at maxCollectionWorkers.
	work := make(chan corev1.ObjectReference, len(clusters))
	for _, cluster := range clusters {
		work <- cluster
	}
	close(work)

	var (
		mu     sync.Mutex
		retErr error
		wg     sync.WaitGroup
	)
	for range numWorkers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for clusterRef := range work {
				select {
				case <-ctx.Done():
					return
				default:
				}
				ref := clusterRef
				if err := collectHealthCheckReportFromCluster(ctx, c, &ref, version, logger); err != nil {
					logErr(&ref, err)
					mu.Lock()
					retErr = err
					mu.Unlock()
				}
			}
		}()
	}
	wg.Wait()

	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return retErr
	}
}

// collectAndProcessAllHealthCheckReports is used in agentless mode. It fetches all
// HealthCheckReports from the management cluster in a single List call, groups them by
// cluster, and processes only clusters that have reports — avoiding N per-cluster List calls.
func collectAndProcessAllHealthCheckReports(ctx context.Context, c client.Client,
	clusterList []corev1.ObjectReference, version string, logger logr.Logger) error {

	shardClusters := buildShardClustersMap(clusterList)

	hcrList := &libsveltosv1beta1.HealthCheckReportList{}
	if err := c.List(ctx, hcrList); err != nil {
		return err
	}

	getLabels := func(h *libsveltosv1beta1.HealthCheckReport) map[string]string { return h.Labels }
	getNamespace := func(h *libsveltosv1beta1.HealthCheckReport) string { return h.Namespace }
	byCluster := groupReportsByCluster(hcrList.Items,
		getLabels, getNamespace,
		libsveltosv1beta1.HealthCheckReportClusterNameLabel,
		libsveltosv1beta1.HealthCheckReportClusterTypeLabel,
		shardClusters)

	var retErr error
	for _, cd := range byCluster {
		l := logger.WithValues("cluster", fmt.Sprintf("%s %s/%s", cd.ref.Kind, cd.ref.Namespace, cd.ref.Name))
		if err := processHealthCheckReportsForClusterInAgentlessMode(ctx, c, &cd.ref, cd.items, version, l); err != nil {
			retErr = err
		}
	}

	return retErr
}

// processHealthCheckReportsForClusterInAgentlessMode handles all HealthCheckReports for a single
// cluster in agentless mode. It checks readiness, version compatibility, and processes each report.
func processHealthCheckReportsForClusterInAgentlessMode(ctx context.Context, c client.Client,
	ref *corev1.ObjectReference, hcrs []*libsveltosv1beta1.HealthCheckReport,
	version string, logger logr.Logger) error {

	clusterRef := &corev1.ObjectReference{
		Namespace:  ref.Namespace,
		Name:       ref.Name,
		APIVersion: ref.APIVersion,
		Kind:       ref.Kind,
	}
	ready, err := clusterproxy.IsClusterReadyToBeConfigured(ctx, c, clusterRef, logger)
	if err != nil {
		logger.V(logs.LogDebug).Info("cluster is not ready yet")
		return err
	}
	if !ready {
		return nil
	}

	if !sveltos_upgrade.IsSveltosAgentVersionCompatible(ctx, c, version, ref.Namespace, ref.Name,
		clusterproxy.GetClusterType(ref), true, logger) {

		logger.V(logs.LogDebug).Info(compatibilityErrorMsg)
		return errors.New(compatibilityErrorMsg)
	}

	var retErr error
	for _, hcr := range hcrs {
		var mgmtHealthCheckReport *libsveltosv1beta1.HealthCheckReport
		if !hcr.DeletionTimestamp.IsZero() {
			logger.V(logs.LogDebug).Info("deleting from management cluster")
			if err := deleteHealthCheckReport(ctx, c, ref, hcr, logger); err != nil {
				logger.V(logs.LogInfo).Error(err, "failed to delete HealthCheckReport in management cluster")
				retErr = err
				continue
			}
		} else {
			logger.V(logs.LogDebug).Info("updating in management cluster")
			mgmtHealthCheckReport, err = updateHealthCheckReport(ctx, c, ref, hcr, logger)
			if err != nil {
				logger.V(logs.LogInfo).Error(err, "failed to update HealthCheckReport in management cluster")
				retErr = err
				continue
			}
		}

		// In agentless mode, the Status of HealthCheckReport in the management cluster will be updated.
		// So set hcr to current version (update otherwise will fail with object has been modified).
		if mgmtHealthCheckReport != nil {
			hcr = mgmtHealthCheckReport
		}

		logger.V(logs.LogDebug).Info("updating HealthCheckReport status")
		phase := libsveltosv1beta1.ReportProcessed
		hcr.Status.Phase = &phase
		if err := c.Status().Update(ctx, hcr); err != nil {
			logger.V(logs.LogInfo).Error(err, "failed to update HealthCheckReport status in management cluster")
			retErr = err
		}
	}

	return retErr
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

		clusterHealthChecks := &libsveltosv1beta1.ClusterHealthCheckList{}
		if err := c.List(ctx, clusterHealthChecks); err != nil {
			logger.V(logs.LogInfo).Error(err, "failed to list ClusterHealthChecks")
			time.Sleep(interval)
			continue
		}

		clusterList, err := clusterproxy.GetListOfClustersForShardKey(ctx, c, "", capiOnboardAnnotation, shardKey, logger)
		if err != nil {
			logger.V(logs.LogInfo).Error(err, "failed to get clusters")
			time.Sleep(interval)
			continue
		}

		// Restrict collection to clusters actually matched by a ClusterHealthCheck.
		// Clusters with no matching ClusterHealthCheck have no HealthCheckReports to collect,
		// so we avoid all per-cluster API calls for them. clustersWithHC is built from
		// ClusterHealthCheck.Status.MatchingClusterRefs without shard awareness, so we
		// intersect with the shard-local clusterList.
		clustersWithHC := buildClustersWithHealthCheck(clusterHealthChecks)
		var clustersToCollect []corev1.ObjectReference
		for i := range clusterList {
			if clustersWithHC[clusterList[i]] {
				clustersToCollect = append(clustersToCollect, clusterList[i])
			}
		}

		// In agentless mode all HealthCheckReports live in the management cluster.
		// A single List call covers every cluster, avoiding N per-cluster API calls.
		if getAgentInMgmtCluster() {
			if err := collectAndProcessAllHealthCheckReports(ctx, c, clustersToCollect, version, logger); err != nil {
				logger.V(logs.LogInfo).Error(err, "failed to collect HealthCheckReports")
			}
		} else {
			if err := processHealthCheckReportClusters(ctx, c, clustersToCollect, version, logger); err != nil {
				logger.V(logs.LogInfo).Error(err, "failed to collect HealthCheckReports")
			}
		}

		time.Sleep(interval)
	}
}

func collectAndProcessHealthCheckReportsFromCluster(ctx context.Context, c client.Client,
	cluster *corev1.ObjectReference, version string, logger logr.Logger) error {

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

	isPullMode, err := clusterproxy.IsClusterInPullMode(ctx, c, cluster.Namespace, cluster.Name,
		clusterproxy.GetClusterType(cluster), logger)
	if err != nil {
		return err
	}

	if !isPullMode && !sveltos_upgrade.IsSveltosAgentVersionCompatible(ctx, c, version, cluster.Namespace, cluster.Name,
		clusterproxy.GetClusterType(clusterRef), getAgentInMgmtCluster(), logger) {

		logger.V(logs.LogDebug).Info(compatibilityErrorMsg)
		return errors.New(compatibilityErrorMsg)
	}

	logger.V(logs.LogDebug).Info("collecting HealthCheckReports from cluster")
	// HealthCheckReports location depends on sveltos-agent: management cluster if it's running there,
	// otherwise managed cluster.
	// For cluster in pull mode, the sveltos-applier copies the HealthCheckReports here
	clusterClient, err := getHealthCheckReportClient(ctx, cluster.Namespace, cluster.Name,
		clusterproxy.GetClusterType(clusterRef), isPullMode, logger)
	if err != nil {
		return err
	}

	var listOptions []client.ListOption
	if getAgentInMgmtCluster() || isPullMode {
		clusterType := clusterproxy.GetClusterType(cluster)
		// If agent is in the management cluster or in pull mode, EventReports for this
		// cluster are also in the management cluuster in the cluster namespace.
		listOptions = []client.ListOption{
			client.InNamespace(cluster.Namespace),
			client.MatchingLabels{
				libsveltosv1beta1.EventReportClusterNameLabel: cluster.Name,
				libsveltosv1beta1.EventReportClusterTypeLabel: strings.ToLower(string(clusterType)),
			},
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

		reprocessing := false
		l := logger.WithValues("healthCheckReport", hcr.Name)
		var mgmtHealthCheckReport *libsveltosv1beta1.HealthCheckReport
		// First update/delete healthCheckReports in managemnent cluster
		if !hcr.DeletionTimestamp.IsZero() {
			logger.V(logs.LogDebug).Info("deleting from management cluster")
			err = deleteHealthCheckReport(ctx, c, cluster, hcr, l)
			if err != nil {
				logger.V(logs.LogInfo).Error(err, "failed to delete HealthCheckReport in management cluster")
			}
			reprocessing = true
		} else {
			logger.V(logs.LogDebug).Info("updating in management cluster")
			mgmtHealthCheckReport, err = updateHealthCheckReport(ctx, c, cluster, hcr, l)
			if err != nil {
				logger.V(logs.LogInfo).Error(err, "failed to update HealthCheckReport in management cluster")
			}
			reprocessing = true
		}
		if !reprocessing {
			continue
		}

		if getAgentInMgmtCluster() {
			if mgmtHealthCheckReport != nil {
				// If in agentless mode, the Status of HealthCheckReport in the management cluster will be updated.
				// So set er to current version (update otherwise will fail with object has been modified)
				hcr = mgmtHealthCheckReport
			}
		}

		logger.V(logs.LogDebug).Info("updating in managed cluster")
		// Update HealthCheckReport Status in managed cluster
		phase := libsveltosv1beta1.ReportProcessed
		hcr.Status.Phase = &phase
		err = clusterClient.Status().Update(ctx, hcr)
		if err != nil {
			logger.V(logs.LogInfo).Error(err, "failed to update HealthCheckReport in managed cluster")
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
		return nil, err
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
