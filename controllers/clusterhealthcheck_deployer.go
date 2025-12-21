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
	"encoding/base64"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"time"

	"github.com/gdexlab/go-render/render"
	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/cluster-api/util"
	"sigs.k8s.io/cluster-api/util/annotations"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/projectsveltos/healthcheck-manager/pkg/scope"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
	"github.com/projectsveltos/libsveltos/lib/clusterproxy"
	"github.com/projectsveltos/libsveltos/lib/k8s_utils"
	logs "github.com/projectsveltos/libsveltos/lib/logsettings"
	"github.com/projectsveltos/libsveltos/lib/pullmode"
	libsveltosset "github.com/projectsveltos/libsveltos/lib/set"
	"github.com/projectsveltos/libsveltos/lib/sharding"
)

const (
	// Namespace where reports will be generated
	ReportNamespace = "projectsveltos"
)

func (r *ClusterHealthCheckReconciler) isClusterAShardMatch(ctx context.Context,
	clusterInfo *libsveltosv1beta1.ClusterInfo) (bool, error) {

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
func (r *ClusterHealthCheckReconciler) deployClusterHealthCheck(ctx context.Context,
	chcScope *scope.ClusterHealthCheckScope, logger logr.Logger) error {

	chc := chcScope.ClusterHealthCheck

	logger.V(logs.LogDebug).Info("request to evaluate/deploy")

	var errorSeen error
	allProcessed := true

	for i := range chc.Status.ClusterConditions {
		c := &chc.Status.ClusterConditions[i]

		shardMatch, err := r.isClusterAShardMatch(ctx, &c.ClusterInfo)
		if err != nil {
			return err
		}

		l := logger.WithValues("cluster", fmt.Sprintf("%s:%s/%s",
			c.ClusterInfo.Cluster.Kind, c.ClusterInfo.Cluster.Namespace, c.ClusterInfo.Cluster.Name))

		var clusterInfo *libsveltosv1beta1.ClusterInfo
		if !shardMatch {
			l.V(logs.LogDebug).Info("cluster is not a shard match")
			// Since cluster is not a shard match, another deployment will deploy and update
			// this specific clusterInfo status. Here we simply return current status.
			if c.ClusterInfo.Status != libsveltosv1beta1.SveltosStatusProvisioned {
				allProcessed = false
			}
			// This is a required parameter. It is set by the deployment matching the
			// cluster shard. if not set yet, set it to empty
			if c.ClusterInfo.Hash == nil {
				str := base64.StdEncoding.EncodeToString([]byte("empty"))
				c.ClusterInfo.Hash = []byte(str)
			}
		} else {
			clusterInfo, err = r.processClusterHealthCheck(ctx, chcScope, &c.ClusterInfo.Cluster, l)
			if err != nil {
				errorSeen = err
			}
			if clusterInfo != nil {
				chc.Status.ClusterConditions[i].ClusterInfo = *clusterInfo
				if clusterInfo.Status != libsveltosv1beta1.SveltosStatusProvisioned {
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
	logger logr.Logger) error {

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

		if !shardMatch && chc.Status.ClusterConditions[i].ClusterInfo.Status != libsveltosv1beta1.SveltosStatusRemoved {
			// If shard is not a match, wait for other controller to remove
			err = fmt.Errorf("remove pending")
			continue
		}

		c := &chc.Status.ClusterConditions[i].ClusterInfo.Cluster

		_, tmpErr = r.removeClusterHealthCheck(ctx, chcScope, c, logger)
		if tmpErr != nil {
			err = tmpErr
			continue
		}
	}

	return err
}

// processClusterHealthCheck detect whether it is needed to deploy ClusterHealthCheck in current passed cluster.
func (r *ClusterHealthCheckReconciler) processClusterHealthCheck(ctx context.Context, chcScope *scope.ClusterHealthCheckScope,
	cluster *corev1.ObjectReference, logger logr.Logger) (*libsveltosv1beta1.ClusterInfo, error) {

	if !isClusterStillMatching(chcScope, cluster) {
		return r.removeClusterHealthCheck(ctx, chcScope, cluster, logger)
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

	// Get the ClusterHealthCheck hash when ClusterHealthCheck was last deployed/evaluated in this cluster (if ever)
	hash, _ := r.getCHCInClusterHashAndStatus(chc, cluster)
	isConfigSame := reflect.DeepEqual(hash, currentHash)
	if !isConfigSame {
		logger.V(logs.LogDebug).Info(fmt.Sprintf("ClusterHealthCheck has changed. Current hash %x. Previous hash %x",
			currentHash, hash))
	}

	if isConfigSame {
		logger.V(logs.LogDebug).Info("clusterhealthcheck has not changed")
	}

	isPullMode, err := clusterproxy.IsClusterInPullMode(ctx, r.Client, cluster.Namespace,
		cluster.Name, clusterproxy.GetClusterType(cluster), logger)
	if err != nil {
		msg := fmt.Sprintf("failed to verify if Cluster is in pull mode: %v", err)
		logger.V(logs.LogDebug).Info(msg)
		return nil, err
	}

	return r.proceedProcessingClusterHealthCheck(ctx, chcScope, cluster, isPullMode, isConfigSame, currentHash,
		logger)
}

func (r *ClusterHealthCheckReconciler) proceedProcessingClusterHealthCheck(ctx context.Context,
	chcScope *scope.ClusterHealthCheckScope, cluster *corev1.ObjectReference, isPullMode, isConfigSame bool,
	currentHash []byte, logger logr.Logger) (*libsveltosv1beta1.ClusterInfo, error) {

	chc := chcScope.ClusterHealthCheck
	_, currentStatus := r.getCHCInClusterHashAndStatus(chc, cluster)

	clusterInfo := &libsveltosv1beta1.ClusterInfo{
		Cluster:        *cluster,
		Hash:           currentHash,
		FailureMessage: nil,
	}

	if isConfigSame && currentStatus != nil && *currentStatus == libsveltosv1beta1.SveltosStatusProvisioned {
		logger.V(logs.LogDebug).Info("already deployed")
		s := libsveltosv1beta1.SveltosStatusProvisioned
		clusterInfo.Status = s
		clusterInfo.FailureMessage = nil
	} else {
		logger.V(logs.LogDebug).Info("provisioning healthCheck")
		s := libsveltosv1beta1.SveltosStatusProvisioning
		clusterInfo.Status = s

		if isPullMode {
			// provisioned here means configuration for sveltos-applier has been successufully prepared.
			// In pull mode, verify now agent has deployed the configuration.
			return r.proceedDeployingCHCnPullMode(ctx, chcScope, cluster, isConfigSame,
				currentHash, logger)
		}

		// Getting here means either ClusterHealthCheck failed to be deployed or ClusterHealthCheck has changed.
		// ClusterHealthCheck must be (re)deployed.
		err := processClusterHealthCheckForCluster(ctx, managementClusterClient, cluster.Namespace, cluster.Name,
			chcScope.ClusterHealthCheck, clusterproxy.GetClusterType(cluster), currentHash, logger)
		if err != nil {
			errorMessage := err.Error()
			clusterInfo.FailureMessage = &errorMessage
			clusterInfo.Status = libsveltosv1beta1.SveltosStatusProvisioning
		} else {
			clusterInfo.Status = libsveltosv1beta1.SveltosStatusProvisioned
			clusterInfo.FailureMessage = nil
		}
	}

	return clusterInfo, nil
}

func (r *ClusterHealthCheckReconciler) proceedDeployingCHCnPullMode(ctx context.Context,
	chcScope *scope.ClusterHealthCheckScope, cluster *corev1.ObjectReference,
	isConfigSame bool, currentHash []byte, logger logr.Logger) (*libsveltosv1beta1.ClusterInfo, error) {

	var pullmodeStatus *libsveltosv1beta1.FeatureStatus

	clusterInfo := &libsveltosv1beta1.ClusterInfo{
		Cluster:        *cluster,
		Hash:           currentHash,
		FailureMessage: nil,
	}

	chc := chcScope.ClusterHealthCheck
	if isConfigSame {
		pullmodeHash, err := pullmode.GetRequestorHash(ctx, getManagementClusterClient(),
			cluster.Namespace, cluster.Name, libsveltosv1beta1.ClusterHealthCheckKind, chc.Name,
			libsveltosv1beta1.FeatureClusterHealthCheck, logger)
		if err != nil {
			if !apierrors.IsNotFound(err) {
				msg := fmt.Sprintf("failed to get pull mode hash: %v", err)
				logger.V(logs.LogDebug).Info(msg)
				errorMsg := err.Error()
				clusterInfo.FailureMessage = &errorMsg
				clusterInfo.Status = libsveltosv1beta1.SveltosStatusProvisioning
				return clusterInfo, err
			}
		} else {
			isConfigSame = reflect.DeepEqual(pullmodeHash, currentHash)
		}
	}

	if isConfigSame {
		// only if configuration hash matches, check if feature is deployed
		logger.V(logs.LogDebug).Info("hash has not changed")
		var err error
		pullmodeStatus, err = r.proceesAgentDeploymentStatus(ctx, chc, cluster, logger)
		if err != nil {
			errorMsg := err.Error()
			clusterInfo.FailureMessage = &errorMsg
			clusterInfo.Status = libsveltosv1beta1.SveltosStatusProvisioning
			return clusterInfo, err
		}
	}

	if pullmodeStatus != nil {
		logger.V(logs.LogDebug).Info(fmt.Sprintf("agent result is available. updating status: %v", *pullmodeStatus))
		switch *pullmodeStatus {
		case libsveltosv1beta1.FeatureStatusProvisioned:
			if err := pullmode.TerminateDeploymentTracking(ctx, r.Client, cluster.Namespace, cluster.Name,
				libsveltosv1beta1.ClusterHealthCheckKind, chc.Name, libsveltosv1beta1.FeatureClusterHealthCheck,
				logger); err != nil {
				logger.V(logs.LogDebug).Info(fmt.Sprintf("failed to terminate tracking: %v", err))
				return nil, err
			}
			clusterInfo.Status = libsveltosv1beta1.SveltosStatusProvisioned
			return clusterInfo, nil
		case libsveltosv1beta1.FeatureStatusProvisioning:
			msg := "agent is provisioning the content"
			logger.V(logs.LogDebug).Info(msg)
			provisioning := libsveltosv1beta1.SveltosStatusProvisioning
			clusterInfo.Status = provisioning
			return clusterInfo, nil
		case libsveltosv1beta1.FeatureStatusFailed:
			logger.V(logs.LogDebug).Info("agent failed provisioning the content")
			failed := libsveltosv1beta1.SveltosStatusFailed
			clusterInfo.Status = failed
		case libsveltosv1beta1.FeatureStatusFailedNonRetriable, libsveltosv1beta1.FeatureStatusRemoving,
			libsveltosv1beta1.FeatureStatusAgentRemoving, libsveltosv1beta1.FeatureStatusRemoved:
			logger.V(logs.LogDebug).Info("proceed deploying")
		}
	} else {
		provisioning := libsveltosv1beta1.SveltosStatusProvisioning
		clusterInfo.Status = provisioning
	}

	// Getting here means either agent failed to deploy feature or configuration has changed.
	// Either way, feature must be (re)deployed. Queue so new configuration for agent is prepared.
	err := processClusterHealthCheckForCluster(ctx, managementClusterClient, cluster.Namespace, cluster.Name,
		chcScope.ClusterHealthCheck, clusterproxy.GetClusterType(cluster), currentHash, logger)
	if err != nil {
		errorMessage := err.Error()
		clusterInfo.Status = libsveltosv1beta1.SveltosStatusProvisioning
		clusterInfo.FailureMessage = &errorMessage
	}

	return clusterInfo, fmt.Errorf("request to deploy queued")
}

// If SveltosCluster is in pull mode, verify whether agent has pulled and successuffly deployed it.
func (r *ClusterHealthCheckReconciler) proceesAgentDeploymentStatus(ctx context.Context,
	chc *libsveltosv1beta1.ClusterHealthCheck, cluster *corev1.ObjectReference, logger logr.Logger,
) (*libsveltosv1beta1.FeatureStatus, error) {

	logger.V(logs.LogDebug).Info("Verify if agent has deployed content and process it")

	status, err := pullmode.GetDeploymentStatus(ctx, r.Client, cluster.Namespace, cluster.Name,
		libsveltosv1beta1.ClusterHealthCheckKind, chc.Name, libsveltosv1beta1.FeatureClusterHealthCheck, logger)

	if err != nil {
		if pullmode.IsProcessingMismatch(err) {
			provisioning := libsveltosv1beta1.FeatureStatusProvisioning
			return &provisioning, nil
		}
		return nil, err
	}

	return status.DeploymentStatus, err
}

func (r *ClusterHealthCheckReconciler) removeClusterHealthCheck(ctx context.Context, chcScope *scope.ClusterHealthCheckScope,
	cluster *corev1.ObjectReference, logger logr.Logger) (*libsveltosv1beta1.ClusterInfo, error) {

	chc := chcScope.ClusterHealthCheck

	logger = logger.WithValues("clusterhealthcheck", chc.Name)
	logger.V(logs.LogDebug).Info("request to undeploy")

	if r.isClusterEntryRemoved(chc, cluster) {
		logger.V(logs.LogDebug).Info("feature is removed")
		// feature is removed. Nothing to do.
		return nil, nil
	}

	clusterInfo := &libsveltosv1beta1.ClusterInfo{
		Cluster: *cluster,
		Status:  libsveltosv1beta1.SveltosStatusRemoving,
		Hash:    nil,
	}

	logger.V(logs.LogDebug).Info("un-deploy")
	err := undeployClusterHealthCheckResourcesFromCluster(ctx, managementClusterClient, cluster.Namespace, cluster.Name,
		chcScope.ClusterHealthCheck, clusterproxy.GetClusterType(cluster), logger)
	if err != nil {
		errorMessage := err.Error()
		clusterInfo.FailureMessage = &errorMessage
	} else {
		if err := removeConditionEntry(ctx, r.Client, cluster.Namespace, cluster.Name,
			clusterproxy.GetClusterType(cluster), chc, logger); err != nil {
			return nil, err
		}
		clusterInfo.Status = libsveltosv1beta1.SveltosStatusRemoved
	}

	return clusterInfo, fmt.Errorf("cleanup request is queued")
}

// getCHCInClusterHashAndStatus returns the hash of the ClusterHealthCheck that was deployed/evaluated in a given
// Cluster (if ever deployed/evaluated)
func (r *ClusterHealthCheckReconciler) getCHCInClusterHashAndStatus(chc *libsveltosv1beta1.ClusterHealthCheck,
	cluster *corev1.ObjectReference) ([]byte, *libsveltosv1beta1.SveltosFeatureStatus) {

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
	chc *libsveltosv1beta1.ClusterHealthCheck) (bool, error) {

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
func (r *ClusterHealthCheckReconciler) isClusterEntryRemoved(chc *libsveltosv1beta1.ClusterHealthCheck,
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
	chc *libsveltosv1beta1.ClusterHealthCheck, cluster *corev1.ObjectReference) ([]byte, error) {

	h := sha256.New()
	var config string
	config += render.AsCode(chc.Spec)

	includeClusterSummaries := false
	for i := range chc.Spec.LivenessChecks {
		if chc.Spec.LivenessChecks[i].Type == libsveltosv1beta1.LivenessTypeAddons {
			includeClusterSummaries = true
			break
		}
	}

	if includeClusterSummaries {
		clusterSummaries, err := fetchClusterSummaries(ctx, c, cluster.Namespace, cluster.Name,
			clusterproxy.GetClusterType(cluster))
		if err != nil {
			return nil, err
		}

		// Sort by namespace, then by name
		sort.Slice(clusterSummaries.Items, func(i, j int) bool {
			if clusterSummaries.Items[i].Namespace != clusterSummaries.Items[j].Namespace {
				return clusterSummaries.Items[i].Namespace < clusterSummaries.Items[j].Namespace
			}
			return clusterSummaries.Items[i].Name < clusterSummaries.Items[j].Name
		})

		for i := range clusterSummaries.Items {
			cs := &clusterSummaries.Items[i]

			// Sort FeatureSummaries by FeatureID
			sort.Slice(cs.Status.FeatureSummaries, func(i, j int) bool {
				return cs.Status.FeatureSummaries[i].FeatureID < cs.Status.FeatureSummaries[j].FeatureID
			})

			config += render.AsCode(cs.Status.FeatureSummaries)
		}
	}

	// When in agentless mode, HealthCheck instances are not copied to managed cluster anymore.
	// This addition ensures the ClusterHealthCheck is redeployed due to the change in deployment location.
	if getAgentInMgmtCluster() {
		config += ("agentless")
	}

	var tmpConfig string
	tmpConfig, err := fetchReferencedResources(ctx, c, chc, cluster)
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
	chc *libsveltosv1beta1.ClusterHealthCheck, cluster *corev1.ObjectReference) (string, error) {

	var config string
	for i := range chc.Spec.LivenessChecks {
		lc := &chc.Spec.LivenessChecks[i]
		if lc.Type == libsveltosv1beta1.LivenessTypeHealthCheck {
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
) (*libsveltosv1beta1.HealthCheck, error) {

	if ref == nil {
		return nil, nil
	}

	healthCheck := &libsveltosv1beta1.HealthCheck{}
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
	clusterNamespace, clusterName string, chc *libsveltosv1beta1.ClusterHealthCheck,
	clusterType libsveltosv1beta1.ClusterType, currentHash []byte, logger logr.Logger) error {

	if !chc.DeletionTimestamp.IsZero() {
		logger.V(logs.LogDebug).Info("clusterHealthCheck marked for deletion")
		return nil
	}

	logger.V(logs.LogDebug).Info("Deploy clusterHealthCheck")

	isPullMode, err := clusterproxy.IsClusterInPullMode(ctx, c, clusterNamespace, clusterName, clusterType, logger)
	if err != nil {
		return err
	}

	start := time.Now()

	err = deployHealthChecks(ctx, c, clusterNamespace, clusterName, clusterType, chc, currentHash, isPullMode, logger)
	if err != nil {
		logger.V(logs.LogDebug).Info("failed to deploy referenced HealthChecks")
		return err
	}

	if !isPullMode {
		// In pull mode, when deploying an EventSource, automatically all state eventSources are removed
		err = removeStaleHealthChecks(ctx, c, clusterNamespace, clusterName, clusterType, chc, logger)
		if err != nil {
			logger.V(logs.LogDebug).Info("failed to remove stale HealthChecks")
			return err
		}
	}

	logger.V(logs.LogDebug).Info("Deployed clusterHealthCheck")
	err = evaluateHealthChecksAndSendNotificationsForCluster(ctx, c, clusterNamespace, clusterName, clusterType,
		chc, logger)
	if err != nil {
		return err
	}

	elapsed := time.Since(start)
	programDuration(elapsed, clusterNamespace, clusterName, libsveltosv1beta1.FeatureClusterHealthCheck, clusterType, logger)

	return nil
}

// evaluateHealthChecksAndSendNotificationsForCluster does following:
// - evaluate all health checks (updating ClusterHealthCheck Status)
// - send notifications
func evaluateHealthChecksAndSendNotificationsForCluster(ctx context.Context, c client.Client,
	clusterNamespace, clusterName string, clusterType libsveltosv1beta1.ClusterType,
	chc *libsveltosv1beta1.ClusterHealthCheck, logger logr.Logger) error {

	logger.V(logs.LogDebug).Info("Evaluate health checks and send Notifications for clusterHealthCheck")

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
	clusterNamespace, clusterName string, chc *libsveltosv1beta1.ClusterHealthCheck,
	clusterType libsveltosv1beta1.ClusterType, logger logr.Logger) error {

	logger = logger.WithValues("clusterhealthcheck", chc.Name)

	logger = logger.WithValues("cluster", fmt.Sprintf("%s:%s/%s", clusterType, clusterNamespace, clusterName))
	logger.V(logs.LogDebug).Info("Undeploy clusterHealthCheck")

	err := removeStaleHealthChecks(ctx, c, clusterNamespace, clusterName, clusterType, chc, logger)
	if err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to remove health checks: %v", err))
		return err
	}

	logger.V(logs.LogDebug).Info("Undeployed clusterHealthCheck")
	return nil
}

// evaluateClusterHealthCheckForCluster re-evaluates all LivenessChecks.
// Returns:
// - list of libsveltosv1beta1.Condition for this cluster;
// - bool indicating whether one of the liveness checks changed status;
// - if an error occurs, returns the error
func evaluateClusterHealthCheckForCluster(ctx context.Context, c client.Client,
	clusterNamespace, clusterName string, clusterType libsveltosv1beta1.ClusterType,
	chc *libsveltosv1beta1.ClusterHealthCheck, logger logr.Logger) ([]libsveltosv1beta1.Condition, bool, error) {

	conditions := make([]libsveltosv1beta1.Condition, len(chc.Spec.LivenessChecks))

	statusChanged := false
	for i := range chc.Spec.LivenessChecks {
		livenessCheck := chc.Spec.LivenessChecks[i]

		conditions[i] = libsveltosv1beta1.Condition{
			Type:               libsveltosv1beta1.ConditionType(getConditionType(&livenessCheck)),
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
			conditions[i].Severity = libsveltosv1beta1.ConditionSeverityWarning
			conditions[i].Message = message
		}
	}

	return conditions, statusChanged, nil
}

// sendNotification sends notifications defined in ClusterHealthCheck.
// if resendAll is set to true, all Notifications are sent. Otherwise only the ones which have not been
// sent yet will be delivered.
func sendNotifications(ctx context.Context, c client.Client, clusterNamespace, clusterName string,
	clusterType libsveltosv1beta1.ClusterType, chc *libsveltosv1beta1.ClusterHealthCheck, resendAll bool,
	conditions []libsveltosv1beta1.Condition, logger logr.Logger) error {

	notificationStatus := make(map[string]libsveltosv1beta1.NotificationStatus)
	// Ignore status if all notifications need to be sent again
	if !resendAll {
		notificationStatus = buildNotificationStatusMap(clusterNamespace, clusterName, clusterType, chc)
	}

	notificationSummaries := make([]libsveltosv1beta1.NotificationSummary, 0)

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
					libsveltosv1beta1.NotificationSummary{
						Name:           n.Name,
						Status:         libsveltosv1beta1.NotificationStatusFailedToDeliver,
						FailureMessage: &failureMessage,
					})
			} else {
				notificationSummaries = append(notificationSummaries,
					libsveltosv1beta1.NotificationSummary{
						Name:   n.Name,
						Status: libsveltosv1beta1.NotificationStatusDelivered,
					})
			}
		} else {
			notificationSummaries = append(notificationSummaries,
				libsveltosv1beta1.NotificationSummary{
					Name:   n.Name,
					Status: libsveltosv1beta1.NotificationStatusDelivered,
				})
		}
	}

	updateNotificationSummariesForCluster(ctx, c, clusterNamespace, clusterName, clusterType, chc,
		notificationSummaries, logger)

	return sendNotificationError
}

// updateConditionsForCluster updates ClusterHealthCheck Status.ClusterConditions with latest
// report on liveness checks for this cluster
func updateConditionsForCluster(ctx context.Context, c client.Client,
	clusterNamespace, clusterName string, clusterType libsveltosv1beta1.ClusterType,
	chc *libsveltosv1beta1.ClusterHealthCheck, conditions []libsveltosv1beta1.Condition, logger logr.Logger) error {

	logger.V(logs.LogDebug).Info("updating clusterhealthcheck clusterConditions")

	updated := false
	for i := range chc.Status.ClusterConditions {
		cc := &chc.Status.ClusterConditions[i]
		if isClusterConditionForCluster(cc, clusterNamespace, clusterName, clusterType) {
			updated = true
			chc.Status.ClusterConditions[i].Conditions = conditions
			break
		}
	}

	if !updated {
		return fmt.Errorf("clusterConditions contains no entry for cluster %s:%s/%s",
			clusterType, clusterNamespace, clusterName)
	}

	return nil
}

// updateNotificationSummariesForCluster updates ClusterHealthCheck Status.NotifiicationSummaries with latest
// report on notification checks
func updateNotificationSummariesForCluster(ctx context.Context, c client.Client, clusterNamespace, clusterName string,
	clusterType libsveltosv1beta1.ClusterType, chc *libsveltosv1beta1.ClusterHealthCheck,
	notificationSummaries []libsveltosv1beta1.NotificationSummary, logger logr.Logger) {

	for i := range chc.Status.ClusterConditions {
		cc := &chc.Status.ClusterConditions[i]
		if isClusterConditionForCluster(cc, clusterNamespace, clusterName, clusterType) {
			chc.Status.ClusterConditions[i].NotificationSummaries = notificationSummaries
		}
	}
}

func removeConditionEntry(ctx context.Context, c client.Client,
	clusterNamespace, clusterName string, clusterType libsveltosv1beta1.ClusterType,
	chc *libsveltosv1beta1.ClusterHealthCheck, logger logr.Logger) error {

	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		currentChc := &libsveltosv1beta1.ClusterHealthCheck{}
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

func remove(s []libsveltosv1beta1.ClusterCondition, i int) []libsveltosv1beta1.ClusterCondition {
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
	clusterNamespace, clusterName string, clusterType libsveltosv1beta1.ClusterType,
	chc *libsveltosv1beta1.ClusterHealthCheck, logger logr.Logger) error {

	// Create a map (for faster indexing) of the HealthChecks currently referenced
	currentReferenced := getReferencedHealthChecks(chc, logger)

	if getAgentInMgmtCluster() {
		leaveEntries := &libsveltosset.Set{}
		if chc.DeletionTimestamp.IsZero() {
			// If removeAll is false and eventTrigger still exists, remove all entries but the one pointing
			// to current referenced EventSource
			leaveEntries = currentReferenced
		}

		return removeHealthCheckFromConfigMap(ctx, c, clusterNamespace, clusterName, clusterType, chc,
			leaveEntries, logger)
	}

	isPullMode, err := clusterproxy.IsClusterInPullMode(ctx, c, clusterNamespace, clusterName,
		clusterType, logger)
	if err != nil {
		msg := fmt.Sprintf("failed to verify if Cluster is in pull mode: %v", err)
		logger.V(logs.LogDebug).Info(msg)
		return err
	}

	if isPullMode {
		return undeployClusterHealthCheckInPullMode(ctx, c, clusterNamespace, clusterName, chc, logger)
	}

	return proceedRemovingStaleHealthChecks(ctx, c, clusterNamespace, clusterName, clusterType,
		chc, currentReferenced, logger)
}

func proceedRemovingStaleHealthChecks(ctx context.Context, c client.Client,
	clusterNamespace, clusterName string, clusterType libsveltosv1beta1.ClusterType,
	chc *libsveltosv1beta1.ClusterHealthCheck, currentReferenced *libsveltosset.Set, logger logr.Logger) error {

	remoteClient, err := clusterproxy.GetKubernetesClient(ctx, c, clusterNamespace, clusterName,
		"", "", clusterType, logger)
	if err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to get managed cluster client: %v", err))
		return err
	}

	healthCheckList := &libsveltosv1beta1.HealthCheckList{}
	err = remoteClient.List(ctx, healthCheckList)
	if err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to get list HealthChecks: %v", err))
		return err
	}

	for i := range healthCheckList.Items {
		hc := &healthCheckList.Items[i]

		objRef := &corev1.ObjectReference{
			APIVersion: libsveltosv1beta1.GroupVersion.String(),
			Kind:       libsveltosv1beta1.HealthCheckKind,
			Name:       hc.Name,
		}

		if currentReferenced.Has(objRef) {
			// healthCheck is still referenced
			continue
		}

		targetGK := schema.GroupKind{
			Group: libsveltosv1beta1.GroupVersion.Group,
			Kind:  libsveltosv1beta1.ClusterHealthCheckKind,
		}

		if !util.IsOwnedByObject(hc, chc, targetGK) {
			continue
		}

		k8s_utils.RemoveOwnerReference(hc, chc)

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

func undeployClusterHealthCheckInPullMode(ctx context.Context, c client.Client,
	clusterNamespace, clusterName string, chc *libsveltosv1beta1.ClusterHealthCheck, logger logr.Logger) error {

	// EvenTrigger follows a strict state machine for resource removal:
	//
	// 1. Create ConfigurationGroup with action=Remove
	// 2. Monitor ConfigurationGroup status:
	//    - Missing ConfigurationGroup = resources successfully removed
	//    - ConfigurationGroup.Status = Removed = resources successfully removed
	var retError error
	agentStatus, err := pullmode.GetRemoveStatus(ctx, c, clusterNamespace, clusterName,
		libsveltosv1beta1.ClusterHealthCheckKind, chc.Name, libsveltosv1beta1.FeatureClusterHealthCheck, logger)
	if err != nil {
		retError = err
	} else if agentStatus != nil {
		if agentStatus.DeploymentStatus != nil && *agentStatus.DeploymentStatus == libsveltosv1beta1.FeatureStatusRemoved {
			logger.V(logs.LogDebug).Info("agent removed content")
			err = pullmode.TerminateDeploymentTracking(ctx, c, clusterNamespace, clusterName,
				libsveltosv1beta1.ClusterHealthCheckKind, chc.Name, libsveltosv1beta1.FeatureClusterHealthCheck, logger)
			if err != nil {
				return err
			}
			return nil
		} else if agentStatus.FailureMessage != nil {
			retError = errors.New(*agentStatus.FailureMessage)
		} else {
			return errors.New("agent is removing classifier instance")
		}
	}

	logger.V(logs.LogDebug).Info("queueing request to un-deploy")
	setters := prepareSetters(chc, nil)
	err = pullmode.RemoveDeployedResources(ctx, c, clusterNamespace, clusterName, libsveltosv1beta1.ClusterHealthCheckKind, chc.Name,
		libsveltosv1beta1.FeatureClusterHealthCheck, logger, setters...)
	if err != nil {
		logger.V(logs.LogDebug).Info(fmt.Sprintf("removeDeployedResources failed: %v", err))
		return err
	}

	if retError != nil {
		return retError
	}

	return fmt.Errorf("agent cleanup request is queued")
}

// deployHealthChecks deploys (creates or updates) all HealthChecks referenced by this ClusterHealthCheck
// instance.
func deployHealthChecks(ctx context.Context, c client.Client,
	clusterNamespace, clusterName string, clusterType libsveltosv1beta1.ClusterType,
	chc *libsveltosv1beta1.ClusterHealthCheck, currentHash []byte, isPullMode bool, logger logr.Logger) error {

	currentReferenced := getReferencedHealthChecks(chc, logger)
	if currentReferenced.Len() == 0 {
		return nil
	}

	if isPullMode {
		// If SveltosCluster is in pull mode, discard all previous staged resources. Those will be regenerated now.
		err := pullmode.DiscardStagedResourcesForDeployment(ctx, c, clusterNamespace, clusterName,
			libsveltosv1beta1.ClusterHealthCheckKind, chc.Name, libsveltosv1beta1.FeatureClusterHealthCheck, logger)
		if err != nil {
			return err
		}
	}

	// classifier installs sveltos-agent and CRDs it needs, including
	// HealthCheck and HealthCheckReport CRDs.

	for i := range chc.Spec.LivenessChecks {
		lc := chc.Spec.LivenessChecks[i]
		err := deployHealthCheck(ctx, c, clusterNamespace, clusterName, clusterType, chc, &lc, isPullMode, logger)
		if err != nil {
			logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to get deploy healthCheck: %v", err))
			return err
		}
	}

	if isPullMode {
		setters := prepareSetters(chc, currentHash)
		err := pullmode.CommitStagedResourcesForDeployment(ctx, c, clusterNamespace, clusterName,
			libsveltosv1beta1.ClusterHealthCheckKind, chc.Name, libsveltosv1beta1.FeatureClusterHealthCheck,
			logger, setters...)
		if err != nil {
			return err
		}
	}

	return nil
}

// deployHealthCheck deploys (creates or updates) referenced HealthCheck and recursively any ConfigMap/Secret
// the HealthCheck references (containing the lua script)
func deployHealthCheck(ctx context.Context, c client.Client, clusterNamespace, clusterName string,
	clusterType libsveltosv1beta1.ClusterType, chc *libsveltosv1beta1.ClusterHealthCheck,
	lc *libsveltosv1beta1.LivenessCheck, isPullMode bool, logger logr.Logger) error {

	if lc.Type != libsveltosv1beta1.LivenessTypeHealthCheck {
		return nil
	}

	if lc.LivenessSourceRef == nil {
		// nothing to do
		return nil
	}

	if lc.LivenessSourceRef.Kind != libsveltosv1beta1.HealthCheckKind ||
		lc.LivenessSourceRef.APIVersion != libsveltosv1beta1.GroupVersion.String() {

		msg := fmt.Sprintf("liveness check %s of type HealthCheck can only reference HealthCheck resource", lc.Name)
		logger.V(logs.LogInfo).Info(msg)
		return fmt.Errorf("%s", msg)
	}

	// Fetch HealthCheck
	healthCheck := &libsveltosv1beta1.HealthCheck{}
	err := c.Get(ctx, types.NamespacedName{Name: lc.LivenessSourceRef.Name}, healthCheck)
	if err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to fetch HealthCheck: %v", err))
		return err
	}

	if getAgentInMgmtCluster() {
		return addHealthCheckToConfigMap(ctx, c, clusterNamespace, clusterName, clusterType,
			chc, healthCheck, logger)
	}

	remoteClient, err := clusterproxy.GetKubernetesClient(ctx, c, clusterNamespace, clusterName,
		"", "", clusterType, logger)
	if err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to get managed cluster client: %v", err))
		return err
	}

	err = createOrUpdateHealthCheck(ctx, remoteClient, chc, healthCheck, clusterNamespace, clusterName,
		isPullMode, logger)
	if err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to create/update HealthCheck: %v", err))
		return err
	}

	return nil
}

func createOrUpdateHealthCheck(ctx context.Context, remoteClient client.Client,
	chc *libsveltosv1beta1.ClusterHealthCheck, healthCheck *libsveltosv1beta1.HealthCheck,
	clusterNamespace, clusterName string, isPullMode bool, logger logr.Logger) error {

	logger = logger.WithValues("healthCheck", healthCheck.Name)

	if !isPullMode {
		currentHealthCheck := &libsveltosv1beta1.HealthCheck{}
		err := remoteClient.Get(context.TODO(), types.NamespacedName{Name: healthCheck.Name}, currentHealthCheck)
		if err == nil {
			logger.V(logs.LogDebug).Info("updating healthCheck")
			currentHealthCheck.Spec = healthCheck.Spec
			// Copy labels. If admin-label is set, sveltos-agent will impersonate
			// ServiceAccount representing the tenant admin when fetching resources
			currentHealthCheck.Labels = healthCheck.Labels
			currentHealthCheck.Annotations = map[string]string{
				libsveltosv1beta1.DeployedBySveltosAnnotation: "true",
			}
			k8s_utils.AddOwnerReference(currentHealthCheck, chc)
			return remoteClient.Update(ctx, currentHealthCheck)
		}

		currentHealthCheck.Name = healthCheck.Name
		currentHealthCheck.Spec = healthCheck.Spec
		// Copy labels. If admin-label is set, sveltos-agent will impersonate
		// ServiceAccount representing the tenant admin when fetching resources
		currentHealthCheck.Labels = healthCheck.Labels
		currentHealthCheck.Annotations = map[string]string{
			libsveltosv1beta1.DeployedBySveltosAnnotation: "true",
		}
		k8s_utils.AddOwnerReference(currentHealthCheck, chc)

		logger.V(logs.LogDebug).Info("creating healthCheck")
		return remoteClient.Create(ctx, currentHealthCheck)
	}

	toDeployHealthCheck := getHealthCheckToDeploy(healthCheck)
	unstructuredObj, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&toDeployHealthCheck)
	if err != nil {
		logger.V(logs.LogDebug).Info(fmt.Sprintf("failed to convert HealthCheck instance to unstructured: %v", err))
	}

	u := &unstructured.Unstructured{}
	u.SetUnstructuredContent(unstructuredObj)

	resources := map[string][]unstructured.Unstructured{}
	resources["healthcheck-instance"] = []unstructured.Unstructured{*u}
	return pullmode.StageResourcesForDeployment(ctx, getManagementClusterClient(), clusterNamespace, clusterName,
		libsveltosv1beta1.HealthCheckReportKind, chc.Name, libsveltosv1beta1.FeatureClusterHealthCheck, resources,
		false, logger)
}

func getHealthCheckToDeploy(healthCheck *libsveltosv1beta1.HealthCheck) *libsveltosv1beta1.HealthCheck {
	toDeploy := &libsveltosv1beta1.HealthCheck{
		ObjectMeta: metav1.ObjectMeta{
			Name: healthCheck.Name,
			Annotations: map[string]string{
				libsveltosv1beta1.DeployedBySveltosAnnotation: "true",
			},
		},
		Spec: healthCheck.Spec,
	}

	addTypeInformationToObject(getManagementClusterScheme(), toDeploy)
	return toDeploy
}

func getReferencedHealthChecks(chc *libsveltosv1beta1.ClusterHealthCheck, logger logr.Logger) *libsveltosset.Set {
	currentReferenced := &libsveltosset.Set{}

	if !chc.DeletionTimestamp.IsZero() {
		// if ClusterHealthCheck is deleted, assume it is not referencing any HealthCheck instance
		return currentReferenced
	}

	for i := range chc.Spec.LivenessChecks {
		lc := chc.Spec.LivenessChecks[i]
		if lc.Type == libsveltosv1beta1.LivenessTypeHealthCheck {
			if lc.LivenessSourceRef != nil {
				currentReferenced.Insert(&corev1.ObjectReference{
					APIVersion: libsveltosv1beta1.GroupVersion.String(), // the only resources that can be referenced is HealthCheck
					Kind:       libsveltosv1beta1.HealthCheckKind,
					Name:       lc.LivenessSourceRef.Name,
				})
			}
		}
	}

	return currentReferenced
}

func prepareSetters(chc *libsveltosv1beta1.ClusterHealthCheck, configurationHash []byte) []pullmode.Option {
	setters := make([]pullmode.Option, 0)
	setters = append(setters, pullmode.WithRequestorHash(configurationHash))
	sourceRef := corev1.ObjectReference{
		APIVersion: chc.APIVersion,
		Kind:       chc.Kind,
		Name:       chc.Name,
		UID:        chc.UID,
	}

	setters = append(setters, pullmode.WithSourceRef(&sourceRef))

	return setters
}
