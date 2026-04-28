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
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
	"github.com/projectsveltos/libsveltos/lib/clusterproxy"
	logs "github.com/projectsveltos/libsveltos/lib/logsettings"
	"github.com/projectsveltos/libsveltos/lib/sveltos_upgrade"
)

//+kubebuilder:rbac:groups=config.projectsveltos.io,resources=clusterprofiles,verbs=get;list;watch
//+kubebuilder:rbac:groups=config.projectsveltos.io,resources=profiles,verbs=get;list;watch

// Periodically collects ReloaderReports from each managed cluster.
func collectReloaderReports(c client.Client, collectionInterval int, shardKey, capiOnboardAnnotation, version string,
	logger logr.Logger) {

	logger.V(logs.LogDebug).Info(fmt.Sprintf("collection time is set to %d seconds", collectionInterval))

	ctx := context.TODO()
	for {
		logger.V(logs.LogDebug).Info("collecting ReloaderReports")
		clusterList, err := clusterproxy.GetListOfClustersForShardKey(ctx, c, "", capiOnboardAnnotation, shardKey, logger)
		if err != nil {
			logger.V(logs.LogInfo).Error(err, "failed to get clusters")
			time.Sleep(time.Duration(collectionInterval) * time.Second)
			continue
		}

		// Restrict collection to clusters that have at least one ClusterProfile or Profile
		// with Reloader=true. Clusters without a reloader profile generate no ReloaderReports.
		clustersWithReloader, err := buildClustersWithReloader(ctx, c)
		if err != nil {
			logger.V(logs.LogInfo).Error(err, "failed to build reloader cluster set")
			time.Sleep(time.Duration(collectionInterval) * time.Second)
			continue
		}
		var clustersToCollect []corev1.ObjectReference
		for i := range clusterList {
			if clustersWithReloader[clusterList[i]] {
				clustersToCollect = append(clustersToCollect, clusterList[i])
			}
		}

		// In agentless mode all ReloaderReports live in the management cluster.
		// A single List call covers every cluster, avoiding N per-cluster API calls.
		if getAgentInMgmtCluster() {
			if err := collectAndProcessAllReloaderReports(ctx, c, clustersToCollect, version, logger); err != nil {
				logger.V(logs.LogInfo).Error(err, "failed to collect ReloaderReports")
			}
		} else {
			for i := range clustersToCollect {
				cluster := &clustersToCollect[i]
				err = collectAndProcessReloaderReportsFromCluster(ctx, c, cluster, version, logger)
				if err != nil {
					logger.V(logs.LogInfo).Error(err,
						fmt.Sprintf("failed to collect ReloaderReports from cluster: %s %s/%s",
							cluster.Kind, cluster.Namespace, cluster.Name))
				}
			}
		}

		time.Sleep(time.Duration(collectionInterval) * time.Second)
	}
}

// collectAndProcessAllReloaderReports is used in agentless mode. It fetches all
// ReloaderReports from the management cluster in a single List call, groups them by
// cluster, and processes only clusters that have reports — avoiding N per-cluster List calls.
func collectAndProcessAllReloaderReports(ctx context.Context, c client.Client,
	clusterList []corev1.ObjectReference, version string, logger logr.Logger) error {

	shardClusters := buildShardClustersMap(clusterList)

	rrList := &libsveltosv1beta1.ReloaderReportList{}
	if err := c.List(ctx, rrList); err != nil {
		return err
	}

	getLabels := func(r *libsveltosv1beta1.ReloaderReport) map[string]string { return r.Labels }
	getNamespace := func(r *libsveltosv1beta1.ReloaderReport) string { return r.Namespace }
	byCluster := groupReportsByCluster(rrList.Items, getLabels, getNamespace,
		libsveltosv1beta1.ReloaderReportClusterNameLabel,
		libsveltosv1beta1.ReloaderReportClusterTypeLabel,
		shardClusters)

	var retErr error
	for _, cd := range byCluster {
		clusterID := fmt.Sprintf("%s %s/%s", cd.ref.Kind, cd.ref.Namespace, cd.ref.Name)
		l := logger.WithValues("cluster", clusterID)
		if err := processReloaderReportsForClusterInAgentlessMode(ctx, c, &cd.ref, cd.items, version, l); err != nil {
			retErr = err
		}
	}

	return retErr
}

// processReloaderReportsForClusterInAgentlessMode handles all ReloaderReports for a single
// cluster in agentless mode. It checks readiness, version compatibility, and processes each report.
func processReloaderReportsForClusterInAgentlessMode(ctx context.Context, c client.Client,
	ref *corev1.ObjectReference, rrs []*libsveltosv1beta1.ReloaderReport,
	version string, logger logr.Logger) error {

	clusterRef := &corev1.ObjectReference{
		Namespace:  ref.Namespace,
		Name:       ref.Name,
		APIVersion: ref.APIVersion,
		Kind:       ref.Kind,
	}
	skip, err := skipCollecting(ctx, c, clusterRef, logger)
	if err != nil {
		return err
	}
	if skip {
		return nil
	}

	if !sveltos_upgrade.IsSveltosAgentVersionCompatible(ctx, c, version, ref.Namespace, ref.Name,
		clusterproxy.GetClusterType(ref), true, logger) {

		logger.V(logs.LogDebug).Info(compatibilityErrorMsg)
		return errors.New(compatibilityErrorMsg)
	}

	var retErr error
	for _, rr := range rrs {
		if !rr.DeletionTimestamp.IsZero() {
			// ReloaderReport controller handles cleanup of deleted reports.
			continue
		}
		l := logger.WithValues("reloaderReport", rr.Name)
		logger.V(logs.LogDebug).Info("updating in management cluster")
		if err := updateReloaderReport(ctx, c, ref, rr, l); err != nil {
			l.V(logs.LogInfo).Error(err, "failed to update ReloaderReport in management cluster")
			retErr = err
			continue
		}
		logger.V(logs.LogDebug).Info("delete ReloaderReport in management cluster")
		if err := c.Delete(ctx, rr); err != nil && !apierrors.IsNotFound(err) {
			l.V(logs.LogInfo).Error(err, "failed to delete ReloaderReport")
			retErr = err
		}
	}

	return retErr
}

// buildClustersWithReloader returns the set of clusters currently matched by at least one
// ClusterProfile or Profile that has Spec.Reloader set to true. Only clusters that have
// an active reloader profile will ever generate ReloaderReports, so this is used to skip
// clusters where no collection is needed.
func buildClustersWithReloader(ctx context.Context, c client.Client) (map[corev1.ObjectReference]bool, error) {
	clusters := make(map[corev1.ObjectReference]bool)

	clusterProfileList := &configv1beta1.ClusterProfileList{}
	if err := c.List(ctx, clusterProfileList); err != nil {
		return nil, err
	}
	for i := range clusterProfileList.Items {
		cp := &clusterProfileList.Items[i]
		if !cp.Spec.Reloader {
			continue
		}
		for j := range cp.Status.MatchingClusterRefs {
			clusters[cp.Status.MatchingClusterRefs[j]] = true
		}
	}

	profileList := &configv1beta1.ProfileList{}
	if err := c.List(ctx, profileList); err != nil {
		return nil, err
	}
	for i := range profileList.Items {
		p := &profileList.Items[i]
		if !p.Spec.Reloader {
			continue
		}
		for j := range p.Status.MatchingClusterRefs {
			clusters[p.Status.MatchingClusterRefs[j]] = true
		}
	}

	return clusters, nil
}

func skipCollecting(ctx context.Context, c client.Client, cluster *corev1.ObjectReference,
	logger logr.Logger) (bool, error) {

	clusterRef := &corev1.ObjectReference{
		Namespace:  cluster.Namespace,
		Name:       cluster.Name,
		APIVersion: cluster.APIVersion,
		Kind:       cluster.Kind,
	}
	ready, err := clusterproxy.IsClusterReadyToBeConfigured(ctx, c, clusterRef, logger)
	if err != nil {
		logger.V(logs.LogDebug).Info("cluster is not ready yet")
		return true, err
	}

	if !ready {
		return true, nil
	}

	paused, err := clusterproxy.IsClusterPaused(ctx, c, cluster.Namespace, cluster.Name,
		clusterproxy.GetClusterType(cluster))
	if err != nil {
		logger.V(logs.LogDebug).Error(err, "failed to verify if cluster is paused")
		return true, err
	}

	if paused {
		return true, nil
	}

	return false, nil
}

func collectAndProcessReloaderReportsFromCluster(ctx context.Context, c client.Client,
	cluster *corev1.ObjectReference, version string, logger logr.Logger) error {

	logger = logger.WithValues("cluster", fmt.Sprintf("%s/%s", cluster.Namespace, cluster.Name))
	skipCollecting, err := skipCollecting(ctx, c, cluster, logger)
	if err != nil {
		return err
	}

	if skipCollecting {
		return nil
	}

	clusterRef := &corev1.ObjectReference{
		Namespace:  cluster.Namespace,
		Name:       cluster.Name,
		APIVersion: cluster.APIVersion,
		Kind:       cluster.Kind,
	}

	isPullMode, err := clusterproxy.IsClusterInPullMode(ctx, c, cluster.Namespace, cluster.Name,
		clusterproxy.GetClusterType(cluster), logger)
	if err != nil {
		return err
	}

	if isPullMode {
		// Nothing to do in pull mode
		return nil
	}

	if !sveltos_upgrade.IsSveltosAgentVersionCompatible(ctx, c, version, cluster.Namespace, cluster.Name,
		clusterproxy.GetClusterType(clusterRef), getAgentInMgmtCluster(), logger) {

		logger.V(logs.LogDebug).Info(compatibilityErrorMsg)
		return errors.New(compatibilityErrorMsg)
	}

	logger.V(logs.LogDebug).Info("collecting ReloaderReports from cluster")

	// ReloaderReport location depends on sveltos-agent: management cluster if it's running there,
	// otherwise managed cluster.
	clusterClient, err := getReloaderReportClient(ctx, cluster.Namespace, cluster.Name,
		clusterproxy.GetClusterType(clusterRef), logger)
	if err != nil {
		return err
	}

	reloaderReportList := libsveltosv1beta1.ReloaderReportList{}
	err = clusterClient.List(ctx, &reloaderReportList)
	if err != nil {
		return err
	}

	for i := range reloaderReportList.Items {
		rr := &reloaderReportList.Items[i]
		l := logger.WithValues("reloaderReport", rr.Name)
		if !rr.DeletionTimestamp.IsZero() {
			// ReloaderReports are deleted automatically by ReloaderReport controller
			// after it processes them
			l.V(logs.LogDebug).Info("mark as deleted. Ignore it")
			continue
		} else {
			l.V(logs.LogDebug).Info("updating in management cluster")
			err = updateReloaderReport(ctx, c, cluster, rr, l)
			if err != nil {
				l.V(logs.LogInfo).Error(err,
					"failed to update ReloaderReport in management cluster")
				continue
			}
			if isPullMode {
				continue
			}
			l.V(logs.LogDebug).Info("delete ReloaderReport in managed cluster")
			err = clusterClient.Delete(ctx, rr)
			if err != nil {
				l.V(logs.LogInfo).Error(err,
					"failed to deletd ReloaderReport in managed cluster")
				continue
			}
		}
	}

	return nil
}

func updateReloaderReport(ctx context.Context, c client.Client, cluster *corev1.ObjectReference,
	reloaderReport *libsveltosv1beta1.ReloaderReport, logger logr.Logger) error {

	if reloaderReport.Spec.ClusterName != "" {
		// if ClusterName is set, this is coming from a
		// managed cluster. If management cluster is in turn
		// managed by another cluster, do not pull those.
		return nil
	}

	clusterType := clusterproxy.GetClusterType(cluster)

	currentReloaderReport := &libsveltosv1beta1.ReloaderReport{}
	err := c.Get(ctx,
		types.NamespacedName{Namespace: cluster.Namespace, Name: reloaderReport.Name},
		currentReloaderReport)
	if err != nil {
		if apierrors.IsNotFound(err) {
			logger.V(logs.LogDebug).Info("create ReloaderReport in management cluster")
			currentReloaderReport.Namespace = cluster.Namespace
			currentReloaderReport.Name = reloaderReport.Name
			currentReloaderReport.Labels = reloaderReport.Labels
			currentReloaderReport.Spec = reloaderReport.Spec
			currentReloaderReport.Spec.ClusterNamespace = cluster.Namespace
			currentReloaderReport.Spec.ClusterName = cluster.Name
			currentReloaderReport.Spec.ClusterType = clusterType
			return c.Create(ctx, currentReloaderReport)
		}
		return err
	}

	logger.V(logs.LogDebug).Info("update ReloaderReport in management cluster")
	currentReloaderReport.Spec = reloaderReport.Spec
	currentReloaderReport.Spec.ClusterNamespace = cluster.Namespace
	currentReloaderReport.Spec.ClusterName = cluster.Name
	currentReloaderReport.Spec.ClusterType = clusterType
	currentReloaderReport.Labels = reloaderReport.Labels
	return c.Update(ctx, currentReloaderReport)
}
