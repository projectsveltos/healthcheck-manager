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
	"crypto/sha256"
	"fmt"
	"reflect"
	"time"

	"github.com/gdexlab/go-render/render"
	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/cluster-api/util"
	"sigs.k8s.io/cluster-api/util/annotations"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/projectsveltos/healthcheck-manager/pkg/scope"
	libsveltosv1alpha1 "github.com/projectsveltos/libsveltos/api/v1alpha1"
	"github.com/projectsveltos/libsveltos/lib/clusterproxy"
	"github.com/projectsveltos/libsveltos/lib/deployer"
	logs "github.com/projectsveltos/libsveltos/lib/logsettings"
	libsveltosset "github.com/projectsveltos/libsveltos/lib/set"
	"github.com/projectsveltos/libsveltos/lib/sharding"
)

const (
	// Namespace where reports will be generated
	ReportNamespace = "projectsveltos"
)

type getCurrentHash func(tx context.Context, c client.Client,
	chc *libsveltosv1alpha1.ClusterHealthCheck, cluster *corev1.ObjectReference) ([]byte, error)

type feature struct {
	id          string
	currentHash getCurrentHash
	deploy      deployer.RequestHandler
	undeploy    deployer.RequestHandler
}

func (r *ClusterHealthCheckReconciler) isClusterAShardMatch(ctx context.Context,
	clusterInfo *libsveltosv1alpha1.ClusterInfo) (bool, error) {

	clusterType := clusterproxy.GetClusterType(&clusterInfo.Cluster)
	cluster, err := clusterproxy.GetCluster(ctx, r.Client, clusterInfo.Cluster.Namespace,
		clusterInfo.Cluster.Name, clusterType)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return true, nil
		}
		return false, err
	}

	return sharding.IsShardAMatch(r.ShardKey, cluster), nil
}

// deployClusterHealthCheck process (if needed) clusterHealthCheck livenesscheck in each matching cluster.
// Eventually deploy necessary resources to managed cluster
func (r *ClusterHealthCheckReconciler) deployClusterHealthCheck(ctx context.Context, chcScope *scope.ClusterHealthCheckScope,
	f feature, logger logr.Logger) error {

	chc := chcScope.ClusterHealthCheck

	logger = logger.WithValues("clusterhealthcheck", chc.Name)
	logger.V(logs.LogDebug).Info("request to evaluate/deploy")

	var errorSeen error
	allProcessed := true

	for i := range chc.Status.ClusterConditions {
		c := chc.Status.ClusterConditions[i]

		shardMatch, err := r.isClusterAShardMatch(ctx, &c.ClusterInfo)
		if err != nil {
			return err
		}

		var clusterInfo *libsveltosv1alpha1.ClusterInfo
		if !shardMatch {
			l := logger.WithValues("cluster", fmt.Sprintf("%s:%s/%s",
				c.ClusterInfo.Cluster.Kind, c.ClusterInfo.Cluster.Namespace, c.ClusterInfo.Cluster.Name))
			l.V(logs.LogDebug).Info("cluster is not a shard match")
			// Since cluster is not a shard match, another deployment will deploy and update
			// this specific clusterInfo status. Here we simply return current status.
			if c.ClusterInfo.Status != libsveltosv1alpha1.SveltosStatusProvisioned {
				allProcessed = false
			}
		} else {
			clusterInfo, err = r.processClusterHealthCheck(ctx, chcScope, &c.ClusterInfo.Cluster, f, logger)
			if err != nil {
				errorSeen = err
			}
			if clusterInfo != nil {
				chc.Status.ClusterConditions[i].ClusterInfo = *clusterInfo
				if clusterInfo.Status != libsveltosv1alpha1.SveltosStatusProvisioned {
					allProcessed = false
				}
			}
		}
	}

	logger.V(logs.LogDebug).Info("set conditions")
	chcScope.SetClusterConditions(chc.Status.ClusterConditions)

	if errorSeen != nil {
		return errorSeen
	}

	if !allProcessed {
		return fmt.Errorf("request to process ClusterHealthCheck is still queued in one ore more clusters")
	}

	return nil
}

func (r *ClusterHealthCheckReconciler) undeployClusterHealthCheck(ctx context.Context, chcScope *scope.ClusterHealthCheckScope,
	f feature, logger logr.Logger) error {

	chc := chcScope.ClusterHealthCheck

	logger = logger.WithValues("clusterhealthcheck", chc.Name)
	logger.V(logs.LogDebug).Info("request to undeploy")

	var err error
	for i := range chc.Status.ClusterConditions {
		shardMatch, tmpErr := r.isClusterAShardMatch(ctx, &chc.Status.ClusterConditions[i].ClusterInfo)
		if tmpErr != nil {
			err = tmpErr
			continue
		}

		if !shardMatch && chc.Status.ClusterConditions[i].ClusterInfo.Status != libsveltosv1alpha1.SveltosStatusRemoved {
			// If shard is not a match, wait for other controller to remove
			err = fmt.Errorf("remove pending")
			continue
		}

		c := &chc.Status.ClusterConditions[i].ClusterInfo.Cluster

		_, tmpErr = r.removeClusterHealthCheck(ctx, chcScope, c, f, logger)
		if tmpErr != nil {
			err = tmpErr
			continue
		}
	}

	return err
}

// processClusterHealthCheck detect whether it is needed to deploy ClusterHealthCheck in current passed cluster.
func (r *ClusterHealthCheckReconciler) processClusterHealthCheck(ctx context.Context, chcScope *scope.ClusterHealthCheckScope,
	cluster *corev1.ObjectReference, f feature, logger logr.Logger,
) (*libsveltosv1alpha1.ClusterInfo, error) {

	if !isClusterStillMatching(chcScope, cluster) {
		return r.removeClusterHealthCheck(ctx, chcScope, cluster, f, logger)
	}

	chc := chcScope.ClusterHealthCheck

	// Get ClusterHealthCheck Spec hash (at this very precise moment)
	currentHash, err := clusterHealthCheckHash(ctx, r.Client, chc, cluster)
	if err != nil {
		return nil, err
	}

	proceed, err := r.canProceed(ctx, chcScope, cluster, logger)
	if err != nil {
		return nil, err
	} else if !proceed {
		return nil, nil
	}

	// If undeploying feature is in progress, wait for it to complete.
	// Otherwise, if we redeploy feature while same feature is still being cleaned up, if two workers process those request in
	// parallel some resources might end up missing.
	if r.Deployer.IsInProgress(cluster.Namespace, cluster.Name, chc.Name, f.id, clusterproxy.GetClusterType(cluster), true) {
		logger.V(logs.LogDebug).Info("cleanup is in progress")
		return nil, fmt.Errorf("cleanup of %s in cluster still in progress. Wait before redeploying", f.id)
	}

	// Get the ClusterHealthCheck hash when ClusterHealthCheck was last deployed/evaluated in this cluster (if ever)
	hash, currentStatus := r.getCHCInClusterHashAndStatus(chc, cluster)
	isConfigSame := reflect.DeepEqual(hash, currentHash)
	if !isConfigSame {
		logger.V(logs.LogDebug).Info(fmt.Sprintf("ClusterHealthCheck has changed. Current hash %x. Previous hash %x",
			currentHash, hash))
	}

	var status *libsveltosv1alpha1.SveltosFeatureStatus
	var result deployer.Result

	if isConfigSame {
		logger.V(logs.LogInfo).Info("clusterhealthcheck has not changed")
		result = r.Deployer.GetResult(ctx, cluster.Namespace, cluster.Name, chc.Name, f.id,
			clusterproxy.GetClusterType(cluster), false)
		status = r.convertResultStatus(result)
	}

	if status != nil {
		logger.V(logs.LogDebug).Info(fmt.Sprintf("result is available %q. updating status.", *status))
		var errorMessage string
		if result.Err != nil {
			errorMessage = result.Err.Error()
		}
		clusterInfo := &libsveltosv1alpha1.ClusterInfo{
			Cluster:        *cluster,
			Status:         *status,
			Hash:           currentHash,
			FailureMessage: &errorMessage,
		}

		if *status == libsveltosv1alpha1.SveltosStatusProvisioned {
			return clusterInfo, nil
		}
		if *status == libsveltosv1alpha1.SveltosStatusProvisioning {
			return clusterInfo, fmt.Errorf("clusterHealthCheck is still being provisioned")
		}
	} else if isConfigSame && currentStatus != nil && *currentStatus == libsveltosv1alpha1.SveltosStatusProvisioned {
		logger.V(logs.LogInfo).Info("already deployed")
		s := libsveltosv1alpha1.SveltosStatusProvisioned
		status = &s
	} else {
		logger.V(logs.LogInfo).Info("no result is available. queue job and mark status as provisioning")
		s := libsveltosv1alpha1.SveltosStatusProvisioning
		status = &s

		// Getting here means either ClusterHealthCheck failed to be deployed or ClusterHealthCheck has changed.
		// ClusterHealthCheck must be (re)deployed.
		if err := r.Deployer.Deploy(ctx, cluster.Namespace, cluster.Name, chc.Name, f.id, clusterproxy.GetClusterType(cluster),
			false, processClusterHealthCheckForCluster, programDuration, deployer.Options{}); err != nil {
			return nil, err
		}
	}

	clusterInfo := &libsveltosv1alpha1.ClusterInfo{
		Cluster:        *cluster,
		Status:         *status,
		Hash:           currentHash,
		FailureMessage: nil,
	}

	return clusterInfo, nil
}

func (r *ClusterHealthCheckReconciler) removeClusterHealthCheck(ctx context.Context, chcScope *scope.ClusterHealthCheckScope,
	cluster *corev1.ObjectReference, f feature, logger logr.Logger) (*libsveltosv1alpha1.ClusterInfo, error) {

	chc := chcScope.ClusterHealthCheck

	logger = logger.WithValues("clusterhealthcheck", chc.Name)
	logger.V(logs.LogDebug).Info("request to undeploy")

	// Remove any queued entry to deploy/evaluate
	r.Deployer.CleanupEntries(cluster.Namespace, cluster.Name, chc.Name, f.id,
		clusterproxy.GetClusterType(cluster), false)

	// If deploying feature is in progress, wait for it to complete.
	// Otherwise, if we cleanup feature while same feature is still being provisioned, if two workers process those request in
	// parallel some resources might be left over.
	if r.Deployer.IsInProgress(cluster.Namespace, cluster.Name, chc.Name, f.id,
		clusterproxy.GetClusterType(cluster), false) {

		logger.V(logs.LogDebug).Info("provisioning is in progress")
		return nil, fmt.Errorf("deploying %s still in progress. Wait before cleanup", f.id)
	}

	if r.isClusterEntryRemoved(chc, cluster) {
		logger.V(logs.LogDebug).Info("feature is removed")
		// feature is removed. Nothing to do.
		return nil, nil
	}

	result := r.Deployer.GetResult(ctx, cluster.Namespace, cluster.Name, chc.Name, f.id,
		clusterproxy.GetClusterType(cluster), true)
	status := r.convertResultStatus(result)

	clusterInfo := &libsveltosv1alpha1.ClusterInfo{
		Cluster: *cluster,
		Status:  libsveltosv1alpha1.SveltosStatusRemoving,
		Hash:    nil,
	}

	if status != nil {
		if *status == libsveltosv1alpha1.SveltosStatusRemoving {
			return clusterInfo, fmt.Errorf("feature is still being removed")
		}

		if *status == libsveltosv1alpha1.SveltosStatusRemoved {
			if err := removeConditionEntry(ctx, r.Client, cluster.Namespace, cluster.Name,
				clusterproxy.GetClusterType(cluster), chc, logger); err != nil {
				return nil, err
			}
			return clusterInfo, nil
		}
	} else {
		logger.V(logs.LogDebug).Info("no result is available. mark status as removing")
	}

	logger.V(logs.LogDebug).Info("queueing request to un-deploy")
	if err := r.Deployer.Deploy(ctx, cluster.Namespace, cluster.Name, chc.Name, f.id,
		clusterproxy.GetClusterType(cluster), true,
		undeployClusterHealthCheckResourcesFromCluster, programDuration, deployer.Options{}); err != nil {
		return nil, err
	}

	return clusterInfo, fmt.Errorf("cleanup request is queued")
}

func (r *ClusterHealthCheckReconciler) convertResultStatus(result deployer.Result) *libsveltosv1alpha1.SveltosFeatureStatus {
	switch result.ResultStatus {
	case deployer.Deployed:
		s := libsveltosv1alpha1.SveltosStatusProvisioned
		return &s
	case deployer.Failed:
		s := libsveltosv1alpha1.SveltosStatusFailed
		return &s
	case deployer.InProgress:
		s := libsveltosv1alpha1.SveltosStatusProvisioning
		return &s
	case deployer.Removed:
		s := libsveltosv1alpha1.SveltosStatusRemoved
		return &s
	case deployer.Unavailable:
		return nil
	}

	return nil
}

// getCHCInClusterHashAndStatus returns the hash of the ClusterHealthCheck that was deployed/evaluated in a given
// Cluster (if ever deployed/evaluated)
func (r *ClusterHealthCheckReconciler) getCHCInClusterHashAndStatus(chc *libsveltosv1alpha1.ClusterHealthCheck,
	cluster *corev1.ObjectReference) ([]byte, *libsveltosv1alpha1.SveltosFeatureStatus) {

	for i := range chc.Status.ClusterConditions {
		cCondition := &chc.Status.ClusterConditions[i]
		if isClusterConditionForCluster(cCondition, cluster.Namespace, cluster.Name, clusterproxy.GetClusterType(cluster)) {
			return cCondition.ClusterInfo.Hash, &cCondition.ClusterInfo.Status
		}
	}

	return nil, nil
}

// isPaused returns true if Sveltos/CAPI Cluster is paused or ClusterHealthCheck has paused annotation.
func (r *ClusterHealthCheckReconciler) isPaused(ctx context.Context, cluster *corev1.ObjectReference,
	chc *libsveltosv1alpha1.ClusterHealthCheck) (bool, error) {

	isClusterPaused, err := clusterproxy.IsClusterPaused(ctx, r.Client, cluster.Namespace, cluster.Name,
		clusterproxy.GetClusterType(cluster))

	if err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}

	if isClusterPaused {
		return true, nil
	}

	return annotations.HasPaused(chc), nil
}

// canProceed returns true if cluster is ready to be programmed and it is not paused.
func (r *ClusterHealthCheckReconciler) canProceed(ctx context.Context, chcScope *scope.ClusterHealthCheckScope,
	cluster *corev1.ObjectReference, logger logr.Logger) (bool, error) {

	logger = logger.WithValues("cluster", fmt.Sprintf("%s:%s/%s", cluster.Kind, cluster.Namespace, cluster.Name))

	paused, err := r.isPaused(ctx, cluster, chcScope.ClusterHealthCheck)
	if err != nil {
		return false, err
	}

	if paused {
		logger.V(logs.LogDebug).Info("Cluster/ClusterHealthCheck is paused")
		return false, nil
	}

	ready, err := clusterproxy.IsClusterReadyToBeConfigured(ctx, r.Client, cluster, chcScope.Logger)
	if err != nil {
		return false, err
	}

	if !ready {
		logger.V(logs.LogInfo).Info("Cluster is not ready yet")
		return false, nil
	}

	return true, nil
}

// isClusterEntryRemoved returns true if feature is there is no entry for cluster in Status.ClusterConditions
func (r *ClusterHealthCheckReconciler) isClusterEntryRemoved(chc *libsveltosv1alpha1.ClusterHealthCheck,
	cluster *corev1.ObjectReference) bool {

	for i := range chc.Status.ClusterConditions {
		cc := &chc.Status.ClusterConditions[i]
		if isClusterConditionForCluster(cc, cluster.Namespace, cluster.Name, clusterproxy.GetClusterType(cluster)) {
			return false
		}
	}
	return true
}

//////////

// clusterHealthCheckHash returns the clusterHealthCheck hash
func clusterHealthCheckHash(ctx context.Context, c client.Client,
	chc *libsveltosv1alpha1.ClusterHealthCheck, cluster *corev1.ObjectReference) ([]byte, error) {

	h := sha256.New()
	var config string
	config += render.AsCode(chc.Spec)

	clusterSummaries, err := fetchClusterSummaries(ctx, c, cluster.Namespace, cluster.Name,
		clusterproxy.GetClusterType(cluster))
	if err != nil {
		return nil, err
	}

	for i := range clusterSummaries.Items {
		cs := &clusterSummaries.Items[i]
		config += render.AsCode(cs.Status.FeatureSummaries)
	}

	var tmpConfig string
	tmpConfig, err = fetchReferencedResources(ctx, c, chc, cluster)
	if err != nil {
		return nil, err
	}
	config += render.AsCode(tmpConfig)

	h.Write([]byte(config))
	return h.Sum(nil), nil
}

// fetchReferencedResources fetches referenced HealthChecks and corresponding
// HealthCheckReports. Returns slice of byte representing those.
func fetchReferencedResources(ctx context.Context, c client.Client,
	chc *libsveltosv1alpha1.ClusterHealthCheck, cluster *corev1.ObjectReference) (string, error) {

	var config string
	for i := range chc.Spec.LivenessChecks {
		lc := &chc.Spec.LivenessChecks[i]
		if lc.Type == libsveltosv1alpha1.LivenessTypeHealthCheck {
			resource, err := fetchHealthCheck(ctx, c, lc.LivenessSourceRef)
			if err != nil {
				return "", err
			}
			if resource == nil {
				continue
			}
			config += render.AsCode(resource.Spec)

			list, err := fetchHealthCheckReports(ctx, c, cluster.Namespace, cluster.Name, resource.Name,
				clusterproxy.GetClusterType(cluster))
			if err != nil {
				return "", err
			}

			for j := range list.Items {
				config += render.AsCode(list.Items[j].Spec)
			}
		}
	}

	return config, nil
}

// fetchHealthCheck fetches referenced HealthCheck
func fetchHealthCheck(ctx context.Context, c client.Client, ref *corev1.ObjectReference,
) (*libsveltosv1alpha1.HealthCheck, error) {

	if ref == nil {
		return nil, nil
	}

	healthCheck := &libsveltosv1alpha1.HealthCheck{}
	err := c.Get(ctx, types.NamespacedName{Name: ref.Name}, healthCheck)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}

	return healthCheck, nil
}

// processClusterHealthCheckForCluster does following:
// - deploy ClusterHealthCheck in cluster if needed (only if one of the liveness checks requires to
// look at resources directly in managed cluster);
func processClusterHealthCheckForCluster(ctx context.Context, c client.Client,
	clusterNamespace, clusterName, applicant, featureID string,
	clusterType libsveltosv1alpha1.ClusterType, options deployer.Options, logger logr.Logger) error {

	logger = logger.WithValues("clusterhealthcheck", applicant)
	logger = logger.WithValues("cluster", fmt.Sprintf("%s:%s/%s", clusterType, clusterNamespace, clusterName))

	chc := &libsveltosv1alpha1.ClusterHealthCheck{}
	err := c.Get(ctx, types.NamespacedName{Name: applicant}, chc)
	if err != nil {
		if apierrors.IsNotFound(err) {
			logger.V(logs.LogDebug).Info("clusterHealthCheck not found")
			return nil
		}
		return err
	}

	if !chc.DeletionTimestamp.IsZero() {
		logger.V(logs.LogDebug).Info("clusterHealthCheck marked for deletion")
		return nil
	}

	logger.V(logs.LogDebug).Info("Deploy clusterHealthCheck")

	err = deployHealthChecks(ctx, c, clusterNamespace, clusterName, clusterType, chc, logger)
	if err != nil {
		logger.V(logs.LogDebug).Info("failed to deploy referenced HealthChecks")
		return err
	}

	err = removeStaleHealthChecks(ctx, c, clusterNamespace, clusterName, clusterType, chc, logger)
	if err != nil {
		logger.V(logs.LogDebug).Info("failed to remove stale HealthChecks")
		return err
	}

	logger.V(logs.LogDebug).Info("Deployed clusterHealthCheck")
	return evaluateLivenessChecksAndSendNotificationsForCluster(ctx, c, clusterNamespace, clusterName, clusterType,
		chc, logger)
}

// evaluateLivenessChecksAndSendNotificationsForCluster does following:
// - evaluate liveness checks (updating ClusterHealthCheck Status)
// - send notifications
func evaluateLivenessChecksAndSendNotificationsForCluster(ctx context.Context, c client.Client,
	clusterNamespace, clusterName string, clusterType libsveltosv1alpha1.ClusterType,
	chc *libsveltosv1alpha1.ClusterHealthCheck, logger logr.Logger) error {

	logger.V(logs.LogDebug).Info("Evaluate LivenessCheck and send Notifications for clusterHealthCheck")

	conditions, changed, err := evaluateClusterHealthCheckForCluster(ctx, c, clusterNamespace, clusterName, clusterType, chc, logger)
	if err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to evaluate livenessChecks: %v", err))
		return err
	}

	err = updateConditionsForCluster(ctx, c, clusterNamespace, clusterName, clusterType, chc, conditions, logger)
	if err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to update conditions: %v", err))
		return err
	}

	return sendNotifications(ctx, c, clusterNamespace, clusterName, clusterType, chc, changed, conditions, logger)
}

// undeployClusterHealthCheckResourcesFromCluster cleans resources associtated with ClusterHealthCheck instance from cluster
func undeployClusterHealthCheckResourcesFromCluster(ctx context.Context, c client.Client,
	clusterNamespace, clusterName, applicant, featureID string,
	clusterType libsveltosv1alpha1.ClusterType, options deployer.Options, logger logr.Logger) error {

	logger = logger.WithValues("clusterhealthcheck", applicant)

	chc := &libsveltosv1alpha1.ClusterHealthCheck{}
	err := c.Get(ctx, types.NamespacedName{Name: applicant}, chc)
	if err != nil {
		if apierrors.IsNotFound(err) {
			logger.V(logs.LogDebug).Info("clusterHealthCheck not found")
			return nil
		}
		return err
	}

	logger = logger.WithValues("cluster", fmt.Sprintf("%s:%s/%s", clusterType, clusterNamespace, clusterName))
	logger.V(logs.LogDebug).Info("Undeploy clusterHealthCheck")

	err = removeStaleHealthChecks(ctx, c, clusterNamespace, clusterName, clusterType, chc, logger)
	if err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to remove health checks: %v", err))
		return err
	}

	logger.V(logs.LogDebug).Info("Undeployed clusterHealthCheck")
	return nil
}

// evaluateClusterHealthCheckForCluster re-evaluates all LivenessChecks.
// Returns:
// - list of libsveltosv1alpha1.Condition for this cluster;
// - bool indicating whether one of the liveness checks changed status;
// - if an error occurs, returns the error
func evaluateClusterHealthCheckForCluster(ctx context.Context, c client.Client,
	clusterNamespace, clusterName string, clusterType libsveltosv1alpha1.ClusterType,
	chc *libsveltosv1alpha1.ClusterHealthCheck, logger logr.Logger) ([]libsveltosv1alpha1.Condition, bool, error) {

	conditions := make([]libsveltosv1alpha1.Condition, len(chc.Spec.LivenessChecks))

	statusChanged := false
	for i := range chc.Spec.LivenessChecks {
		livenessCheck := chc.Spec.LivenessChecks[i]

		conditions[i] = libsveltosv1alpha1.Condition{
			Type:               libsveltosv1alpha1.ConditionType(getConditionType(&livenessCheck)),
			LastTransitionTime: metav1.Time{Time: time.Now()},
		}

		var tmpStatusChanged bool
		passing, tmpStatusChanged, message, err := evaluateLivenessCheck(ctx, c, clusterNamespace, clusterName, clusterType, chc,
			&livenessCheck, logger)
		if err != nil {
			logger.V(logs.LogDebug).Info("failed to evaluate livenessCheck %v. Err: %v", livenessCheck, err)
			return nil, false, err
		}
		if tmpStatusChanged {
			statusChanged = true
		}

		conditions[i].Name = livenessCheck.Name
		conditions[i].Status = getConditionStatus(passing)
		if !passing {
			conditions[i].Severity = libsveltosv1alpha1.ConditionSeverityWarning
			conditions[i].Message = message
		}
	}

	return conditions, statusChanged, nil
}

// sendNotification sends notifications defined in ClusterHealthCheck.
// if resendAll is set to true, all Notifications are sent. Otherwise only the ones which have not been
// sent yet will be delivered.
func sendNotifications(ctx context.Context, c client.Client, clusterNamespace, clusterName string,
	clusterType libsveltosv1alpha1.ClusterType, chc *libsveltosv1alpha1.ClusterHealthCheck, resendAll bool,
	conditions []libsveltosv1alpha1.Condition, logger logr.Logger) error {

	notificationStatus := make(map[string]libsveltosv1alpha1.NotificationStatus)
	// Ignore status if all notifications need to be sent again
	if !resendAll {
		notificationStatus = buildNotificationStatusMap(clusterNamespace, clusterName, clusterType, chc)
	}

	notificationSummaries := make([]libsveltosv1alpha1.NotificationSummary, 0)

	var sendNotificationError error
	for i := range chc.Spec.Notifications {
		n := &chc.Spec.Notifications[i]
		if doSendNotification(n, notificationStatus, resendAll) {
			if err := sendNotification(ctx, c, clusterNamespace, clusterName, clusterType,
				chc, n, conditions, logger); err != nil {
				logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to deliver notification %s:%s. Err: %v",
					n.Type, n.Name, err))
				sendNotificationError = err
				failureMessage := err.Error()
				notificationSummaries = append(notificationSummaries,
					libsveltosv1alpha1.NotificationSummary{
						Name:           n.Name,
						Status:         libsveltosv1alpha1.NotificationStatusFailedToDeliver,
						FailureMessage: &failureMessage,
					})
			} else {
				notificationSummaries = append(notificationSummaries,
					libsveltosv1alpha1.NotificationSummary{
						Name:   n.Name,
						Status: libsveltosv1alpha1.NotificationStatusDelivered,
					})
			}
		} else {
			notificationSummaries = append(notificationSummaries,
				libsveltosv1alpha1.NotificationSummary{
					Name:   n.Name,
					Status: libsveltosv1alpha1.NotificationStatusDelivered,
				})
		}
	}

	if err := updateNotificationSummariesForCluster(ctx, c, clusterNamespace, clusterName, clusterType, chc,
		notificationSummaries, logger); err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to update notification summaries: %v", err))
	}

	return sendNotificationError
}

// updateConditionsForCluster updates ClusterHealthCheck Status.ClusterConditions with latest
// report on liveness checks for this cluster
func updateConditionsForCluster(ctx context.Context, c client.Client,
	clusterNamespace, clusterName string, clusterType libsveltosv1alpha1.ClusterType,
	chc *libsveltosv1alpha1.ClusterHealthCheck, conditions []libsveltosv1alpha1.Condition, logger logr.Logger) error {

	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		logger.V(logs.LogDebug).Info("updating clusterhealthcheck clusterConditions")
		currentChc := &libsveltosv1alpha1.ClusterHealthCheck{}
		err := c.Get(ctx, types.NamespacedName{Name: chc.Name}, currentChc)
		if err != nil {
			return err
		}

		updated := false
		for i := range currentChc.Status.ClusterConditions {
			cc := &currentChc.Status.ClusterConditions[i]
			if isClusterConditionForCluster(cc, clusterNamespace, clusterName, clusterType) {
				updated = true
				currentChc.Status.ClusterConditions[i].Conditions = conditions
			}
		}

		if !updated {
			return fmt.Errorf("clusterConditions contains no entry for cluster %s:%s/%s",
				clusterType, clusterNamespace, clusterName)
		}

		return c.Status().Update(context.TODO(), currentChc)
	})

	return err
}

// updateNotificationSummariesForCluster updates ClusterHealthCheck Status.NotifiicationSummaries with latest
// report on notification checks
func updateNotificationSummariesForCluster(ctx context.Context, c client.Client, clusterNamespace, clusterName string,
	clusterType libsveltosv1alpha1.ClusterType, chc *libsveltosv1alpha1.ClusterHealthCheck,
	notificationSummaries []libsveltosv1alpha1.NotificationSummary, logger logr.Logger) error {

	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		logger.V(logs.LogDebug).Info("updating clusterhealthcheck notificationSummaries")
		currentChc := &libsveltosv1alpha1.ClusterHealthCheck{}
		err := c.Get(ctx, types.NamespacedName{Name: chc.Name}, currentChc)
		if err != nil {
			return err
		}

		updated := false
		for i := range currentChc.Status.ClusterConditions {
			cc := &currentChc.Status.ClusterConditions[i]
			if isClusterConditionForCluster(cc, clusterNamespace, clusterName, clusterType) {
				updated = true
				currentChc.Status.ClusterConditions[i].NotificationSummaries = notificationSummaries
			}
		}

		if !updated {
			return fmt.Errorf("clusterConditions contains no entry for cluster %s:%s/%s",
				clusterType, clusterNamespace, clusterName)
		}
		return c.Status().Update(context.TODO(), currentChc)
	})

	return err
}

func removeConditionEntry(ctx context.Context, c client.Client,
	clusterNamespace, clusterName string, clusterType libsveltosv1alpha1.ClusterType,
	chc *libsveltosv1alpha1.ClusterHealthCheck, logger logr.Logger) error {

	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		currentChc := &libsveltosv1alpha1.ClusterHealthCheck{}
		err := c.Get(ctx, types.NamespacedName{Name: chc.Name}, currentChc)
		if err != nil {
			return err
		}

		for i := range currentChc.Status.ClusterConditions {
			cc := &currentChc.Status.ClusterConditions[i]
			if isClusterConditionForCluster(cc, clusterNamespace, clusterName, clusterType) {
				currentChc.Status.ClusterConditions = remove(currentChc.Status.ClusterConditions, i)
				return c.Status().Update(context.TODO(), currentChc)
			}
		}

		return nil
	})

	return err
}

func remove(s []libsveltosv1alpha1.ClusterCondition, i int) []libsveltosv1alpha1.ClusterCondition {
	s[i] = s[len(s)-1]
	return s[:len(s)-1]
}

// isClusterStillMatching returns true if cluster is still matching by looking at ClusterHealthCheck
// Status MatchingClusterRefs
func isClusterStillMatching(chcScope *scope.ClusterHealthCheckScope, cluster *corev1.ObjectReference) bool {
	for i := range chcScope.ClusterHealthCheck.Status.MatchingClusterRefs {
		matchingCluster := &chcScope.ClusterHealthCheck.Status.MatchingClusterRefs[i]
		if reflect.DeepEqual(*matchingCluster, *cluster) {
			return true
		}
	}
	return false
}

// removeStaleHealthChecks removes stale HealthChecks.
// - If ClusterHealthCheck is deleted, ClusterHealthCheck will be removed as OwnerReference from any
// HealthCheck instance;
// - If ClusterHealthCheck is still existing, ClusterHealthCheck will be removed as OwnerReference from any
// HealthCheck instance it used to referenced and it is not referencing anymore.
// An HealthCheck with zero OwnerReference will be deleted from managed cluster.
func removeStaleHealthChecks(ctx context.Context, c client.Client,
	clusterNamespace, clusterName string, clusterType libsveltosv1alpha1.ClusterType,
	chc *libsveltosv1alpha1.ClusterHealthCheck, logger logr.Logger) error {

	remoteClient, err := clusterproxy.GetKubernetesClient(ctx, c, clusterNamespace, clusterName,
		"", "", clusterType, logger)
	if err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to get managed cluster client: %v", err))
		return err
	}

	healthCheckList := &libsveltosv1alpha1.HealthCheckList{}
	err = remoteClient.List(ctx, healthCheckList)
	if err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to get list HealthChecks: %v", err))
		return err
	}

	// Create a map (for faster indexing) of the HealthChecks currently referenced
	currentReferenced := getReferencedHealthChecks(chc, logger)

	for i := range healthCheckList.Items {
		hc := &healthCheckList.Items[i]

		objRef := &corev1.ObjectReference{
			APIVersion: libsveltosv1alpha1.GroupVersion.String(),
			Kind:       libsveltosv1alpha1.HealthCheckKind,
			Name:       hc.Name,
		}

		if currentReferenced.Has(objRef) {
			// healthCheck is still referenced
			continue
		}

		if !util.IsOwnedByObject(hc, chc) {
			continue
		}

		deployer.RemoveOwnerReference(hc, chc)

		if len(hc.GetOwnerReferences()) != 0 {
			// Other ClusterHealthChecks are still deploying this very same policy
			err = remoteClient.Update(ctx, hc)
			if err != nil {
				logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to get update HealthCheck: %v", err))
				return err
			}
			continue
		}

		err = remoteClient.Delete(ctx, hc)
		if err != nil {
			logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to get delete HealthCheck: %v", err))
			return err
		}
	}

	return nil
}

// deployHealthChecks deploys (creates or updates) all HealthChecks referenced by this ClusterHealthCheck
// instance.
func deployHealthChecks(ctx context.Context, c client.Client,
	clusterNamespace, clusterName string, clusterType libsveltosv1alpha1.ClusterType,
	chc *libsveltosv1alpha1.ClusterHealthCheck, logger logr.Logger) error {

	currentReferenced := getReferencedHealthChecks(chc, logger)
	if currentReferenced.Len() == 0 {
		return nil
	}

	remoteClient, err := clusterproxy.GetKubernetesClient(ctx, c, clusterNamespace, clusterName,
		"", "", clusterType, logger)
	if err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to get managed cluster client: %v", err))
		return err
	}

	// classifier installs sveltos-agent and CRDs it needs, including
	// HealthCheck and HealthCheckReport CRDs.

	for i := range chc.Spec.LivenessChecks {
		lc := chc.Spec.LivenessChecks[i]
		err = deployHealthCheck(ctx, c, remoteClient, chc, &lc, logger)
		if err != nil {
			logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to get deploy healthCheck: %v", err))
			return err
		}
	}

	return nil
}

// deployHealthCheck deploys (creates or updates) referenced HealthCheck and recursively any ConfigMap/Secret
// the HealthCheck references (containing the lua script)
func deployHealthCheck(ctx context.Context, c, remoteClient client.Client, chc *libsveltosv1alpha1.ClusterHealthCheck,
	lc *libsveltosv1alpha1.LivenessCheck, logger logr.Logger) error {

	if lc.Type != libsveltosv1alpha1.LivenessTypeHealthCheck {
		return nil
	}

	if lc.LivenessSourceRef == nil {
		// nothing to do
		return nil
	}

	if lc.LivenessSourceRef.Kind != libsveltosv1alpha1.HealthCheckKind ||
		lc.LivenessSourceRef.APIVersion != libsveltosv1alpha1.GroupVersion.String() {

		msg := fmt.Sprintf("liveness check %s of type HealthCheck can only reference HealthCheck resource", lc.Name)
		logger.V(logs.LogInfo).Info(msg)
		return fmt.Errorf("%s", msg)
	}

	// Fetch HealthCheck
	healthCheck := &libsveltosv1alpha1.HealthCheck{}
	err := c.Get(ctx, types.NamespacedName{Name: lc.LivenessSourceRef.Name}, healthCheck)
	if err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to fetch HealthCheck: %v", err))
		return err
	}

	err = createOrUpdateHealthCheck(ctx, remoteClient, chc, healthCheck, logger)
	if err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to create/update HealthCheck: %v", err))
		return err
	}

	return nil
}

func createOrUpdateHealthCheck(ctx context.Context, remoteClient client.Client, chc *libsveltosv1alpha1.ClusterHealthCheck,
	healthCheck *libsveltosv1alpha1.HealthCheck, logger logr.Logger) error {

	logger = logger.WithValues("healthCheck", healthCheck.Name)

	currentHealthCheck := &libsveltosv1alpha1.HealthCheck{}
	err := remoteClient.Get(context.TODO(), types.NamespacedName{Name: healthCheck.Name}, currentHealthCheck)
	if err == nil {
		logger.V(logs.LogDebug).Info("updating healthCheck")
		currentHealthCheck.Spec = healthCheck.Spec
		// Copy labels. If admin-label is set, sveltos-agent will impersonate
		// ServiceAccount representing the tenant admin when fetching resources
		currentHealthCheck.Labels = healthCheck.Labels
		currentHealthCheck.Annotations = map[string]string{
			libsveltosv1alpha1.DeployedBySveltosAnnotation: "true",
		}
		deployer.AddOwnerReference(currentHealthCheck, chc)
		return remoteClient.Update(ctx, currentHealthCheck)
	}

	currentHealthCheck.Name = healthCheck.Name
	currentHealthCheck.Spec = healthCheck.Spec
	// Copy labels. If admin-label is set, sveltos-agent will impersonate
	// ServiceAccount representing the tenant admin when fetching resources
	currentHealthCheck.Labels = healthCheck.Labels
	currentHealthCheck.Annotations = map[string]string{
		libsveltosv1alpha1.DeployedBySveltosAnnotation: "true",
	}
	deployer.AddOwnerReference(currentHealthCheck, chc)

	logger.V(logs.LogDebug).Info("creating healthCheck")
	return remoteClient.Create(ctx, currentHealthCheck)
}

func getReferencedHealthChecks(chc *libsveltosv1alpha1.ClusterHealthCheck, logger logr.Logger) *libsveltosset.Set {
	currentReferenced := &libsveltosset.Set{}

	if !chc.DeletionTimestamp.IsZero() {
		// if ClusterHealthCheck is deleted, assume it is not referencing any HealthCheck instance
		return currentReferenced
	}

	for i := range chc.Spec.LivenessChecks {
		lc := chc.Spec.LivenessChecks[i]
		if lc.Type == libsveltosv1alpha1.LivenessTypeHealthCheck {
			if lc.LivenessSourceRef != nil {
				currentReferenced.Insert(&corev1.ObjectReference{
					APIVersion: libsveltosv1alpha1.GroupVersion.String(), // the only resources that can be referenced is HealthCheck
					Kind:       libsveltosv1alpha1.HealthCheckKind,
					Name:       lc.LivenessSourceRef.Name,
				})
			}
		}
	}

	return currentReferenced
}
