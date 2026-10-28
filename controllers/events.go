/*
Copyright 2026. projectsveltos.io. All rights reserved.

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
	"sync"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
	"github.com/projectsveltos/libsveltos/lib/clusterproxy"
	"github.com/projectsveltos/libsveltos/lib/eventreasons"
)

// The failure messages of a cluster which are not a failure to deploy the ClusterHealthCheck: the cluster is
// paused, not ready or down, its agent is not healthy, a previous deployment or cleanup is still in progress, or
// the agent of a cluster in pull mode has not removed the resources yet. They are not recorded as events.
const (
	msgClusterPausedOrNotReady = "cannot proceed deploying. Either cluster is paused or not ready."
	msgAgentNotHealthy         = "agent in managed cluster is not healthy."
	msgCleanupInProgress       = "cleanup is in progress"
	msgProvisioningInProgress  = "provisioning is in progress"
	msgAgentCleanupQueued      = "agent cleanup request is queued"
	msgAgentRemoving           = "agent is removing ClusterHealthCheck resources"
)

// failureKey identifies the cluster a ClusterHealthCheck is deployed to.
type failureKey struct {
	clusterHealthCheck string
	clusterKind        string
	clusterNamespace   string
	clusterName        string
}

// failureTracker remembers, for each cluster of each ClusterHealthCheck, the failure message last recorded as
// an event, so that a failure which repeats is recorded once.
// The failure message of the ClusterInfo cannot be used for that: it is cleared when the deployment is queued
// again, so a failure which repeats would look new every time. The tracker is in memory: after the controller
// restarts, a failure which is still there is recorded once more.
type failureTracker struct {
	mu       sync.Mutex
	messages map[failureKey]string
}

// isNew returns true when message is different from the last one recorded for key, and remembers it.
func (t *failureTracker) isNew(key failureKey, message string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()

	if last, ok := t.messages[key]; ok && last == message {
		return false
	}

	t.messages[key] = message
	return true
}

// forget removes what was recorded for key: the next failure is new again.
func (t *failureTracker) forget(key failureKey) {
	t.mu.Lock()
	defer t.mu.Unlock()

	delete(t.messages, key)
}

var (
	recordedFailures = &failureTracker{messages: map[failureKey]string{}}
)

// recordEventf records an event on regarding, about the related object (can be nil).
// It does nothing when no recorder is set.
// The note is capped, so an oversized message is not rejected by the API server.
func recordEventf(regarding, related runtime.Object, eventType, reason, action, format string, args ...any) {
	recorder := getManagementRecorder()
	if recorder == nil {
		return
	}

	note := eventreasons.TruncateNote(fmt.Sprintf(format, args...))
	recorder.Eventf(regarding, related, eventType, reason, action, "%s", note)
}

// getRelatedCluster returns the cluster as the related object of an event, nil if it cannot be fetched.
// The cluster is the related object because the events recorder merges events with the same reason, action and
// regarding object and keeps only the first note: without it the failures of one ClusterHealthCheck in two
// clusters would be a single event.
func getRelatedCluster(ctx context.Context, c client.Client, clusterNamespace, clusterName string,
	clusterType libsveltosv1beta1.ClusterType) runtime.Object {

	cluster, err := clusterproxy.GetCluster(ctx, c, clusterNamespace, clusterName, clusterType)
	if err != nil {
		return nil
	}

	return cluster
}

// recordClusterInfoFailure records a Warning ClusterHealthCheckFailed event on the ClusterHealthCheck when
// deploying it to a cluster, or removing it from a cluster, failed. clusterInfo is what the ClusterHealthCheck is
// going to record for the cluster. Nothing is recorded when the cluster fails again with the message already
// recorded: a retry which fails the same way is not news. A different message is recorded.
// It is called where the cluster info is saved, so every failure message is covered, whichever step of the
// processing set it.
// A cluster which is paused, not ready or down, and a cluster whose agent is not healthy, are not a failure to
// deploy: nothing is recorded for them.
func recordClusterInfoFailure(ctx context.Context, c client.Client, chc *libsveltosv1beta1.ClusterHealthCheck,
	clusterInfo *libsveltosv1beta1.ClusterInfo) {

	cluster := &clusterInfo.Cluster
	clusterType := clusterproxy.GetClusterType(cluster)
	key := failureKey{
		clusterHealthCheck: chc.Name,
		clusterKind:        cluster.Kind,
		clusterNamespace:   cluster.Namespace,
		clusterName:        cluster.Name,
	}

	if clusterInfo.Status == libsveltosv1beta1.SveltosStatusProvisioned ||
		clusterInfo.Status == libsveltosv1beta1.SveltosStatusRemoved {

		recordedFailures.forget(key)
		return
	}

	if clusterInfo.FailureMessage == nil {
		return
	}

	message := *clusterInfo.FailureMessage
	if message == "" || isNotAFailure(message) {
		return
	}

	if !recordedFailures.isNew(key, message) {
		return
	}

	related := getRelatedCluster(ctx, c, cluster.Namespace, cluster.Name, clusterType)

	if clusterInfo.Status == libsveltosv1beta1.SveltosStatusRemoving {
		recordEventf(chc, related, corev1.EventTypeWarning, eventreasons.ClusterHealthCheckFailed,
			eventreasons.ActionRemoving, "Removing from cluster %s %s/%s failed: %s",
			clusterType, cluster.Namespace, cluster.Name, message)
		return
	}

	recordEventf(chc, related, corev1.EventTypeWarning, eventreasons.ClusterHealthCheckFailed,
		eventreasons.ActionDeploying, "Deploying to cluster %s %s/%s failed: %s",
		clusterType, cluster.Namespace, cluster.Name, message)
}

// isNotAFailure returns true for the failure messages which only say the cluster is not in a state where the
// ClusterHealthCheck can be deployed: they are not a failure of the deployment.
func isNotAFailure(message string) bool {
	return message == msgClusterPausedOrNotReady ||
		message == msgAgentNotHealthy ||
		message == msgCleanupInProgress ||
		message == msgProvisioningInProgress ||
		message == msgAgentCleanupQueued ||
		message == msgAgentRemoving
}

// recordNotificationFailure records a Warning NotificationFailed event on the ClusterHealthCheck when a
// notification could not be delivered for a cluster. previous is the summary the ClusterHealthCheck recorded for
// the notification before this evaluation: nothing is recorded when the delivery fails with the message it
// already recorded, a notification is delivered again until it succeeds and a failure which repeats is not
// news. A different message is recorded.
func recordNotificationFailure(ctx context.Context, c client.Client, chc *libsveltosv1beta1.ClusterHealthCheck,
	clusterNamespace, clusterName string, clusterType libsveltosv1beta1.ClusterType,
	n *libsveltosv1beta1.Notification, previous *libsveltosv1beta1.NotificationSummary,
	summary *libsveltosv1beta1.NotificationSummary) {

	if summary.Status != libsveltosv1beta1.NotificationStatusFailedToDeliver || summary.FailureMessage == nil {
		return
	}

	message := *summary.FailureMessage
	if previous != nil && previous.FailureMessage != nil && *previous.FailureMessage == message {
		return
	}

	related := getRelatedCluster(ctx, c, clusterNamespace, clusterName, clusterType)

	recordEventf(chc, related, corev1.EventTypeWarning, eventreasons.NotificationFailed,
		eventreasons.ActionNotifying, "Delivering notification %s (%s) for cluster %s %s/%s failed: %s",
		n.Name, n.Type, clusterType, clusterNamespace, clusterName, message)
}
