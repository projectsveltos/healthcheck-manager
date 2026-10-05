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
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
)

const (
	// messageHashLength is the number of characters of the hash of a failure message kept in the status
	messageHashLength = 16
)

// clusterState is the state of a cluster, as far as notification policies are concerned
type clusterState struct {
	// failing is true when at least one liveness check is not passing
	failing bool
	// messageHash identifies the failure message. Empty when not failing.
	messageHash string
	// failingSince is when the oldest of the currently failing liveness checks started failing
	failingSince time.Time
}

// hasNotificationPolicy returns true when the notification has a policy with at least one setting
func hasNotificationPolicy(n *libsveltosv1beta1.Notification) bool {
	p := n.Policy
	return p != nil && (p.OnlyOnTransition || isPositive(p.MinInterval) || isPositive(p.FailingFor))
}

func isPositive(d *metav1.Duration) bool {
	return d != nil && d.Duration > 0
}

// getClusterState returns the state of a cluster given the result of its liveness checks
func getClusterState(conditions []libsveltosv1beta1.Condition) clusterState {
	state := clusterState{}

	hash := sha256.New()
	for i := range conditions {
		c := &conditions[i]
		if c.Status == corev1.ConditionTrue {
			continue
		}

		state.failing = true
		if state.failingSince.IsZero() || c.LastTransitionTime.Time.Before(state.failingSince) {
			state.failingSince = c.LastTransitionTime.Time
		}
		// Conditions follow the order of the liveness checks, which is stable
		fmt.Fprintf(hash, "%s\x00%s\x00", c.Type, c.Message)
	}

	if state.failing {
		state.messageHash = hex.EncodeToString(hash.Sum(nil))[:messageHashLength]
	}

	return state
}

// evaluateNotificationPolicy decides whether a notification with a policy has to be delivered now.
// summary is what was recorded the last time the notification was evaluated for the cluster. Nil if never.
// It returns:
//   - send: true if the notification has to be delivered now
//   - wait: if send is false and the notification is only being held back, how long to wait before
//     evaluating again. Zero if nothing is pending.
func evaluateNotificationPolicy(policy *libsveltosv1beta1.NotificationPolicy,
	summary *libsveltosv1beta1.NotificationSummary, state clusterState, now time.Time) (send bool, wait time.Duration) {

	lastDelivered := summary != nil && summary.LastSentTime != nil && summary.LastSentFailing != nil

	if !hasDifferenceToReport(policy, summary, state, lastDelivered) {
		return false, 0
	}

	// A failure has to last FailingFor before it is reported
	enteringFailure := state.failing && (!lastDelivered || !*summary.LastSentFailing)
	if enteringFailure && isPositive(policy.FailingFor) {
		failingFor := now.Sub(state.failingSince)
		if failingFor < policy.FailingFor.Duration {
			return false, policy.FailingFor.Duration - failingFor
		}
	}

	// A previous delivery failure is retried without waiting for MinInterval
	previousDeliveryFailed := summary != nil && summary.Status == libsveltosv1beta1.NotificationStatusFailedToDeliver
	if lastDelivered && isPositive(policy.MinInterval) && !previousDeliveryFailed {
		sinceLastDelivery := now.Sub(summary.LastSentTime.Time)
		if sinceLastDelivery < policy.MinInterval.Duration {
			return false, policy.MinInterval.Duration - sinceLastDelivery
		}
	}

	return true, 0
}

// hasDifferenceToReport returns true if the state of the cluster differs from what was last delivered
// in a way that this policy wants to be notified about.
func hasDifferenceToReport(policy *libsveltosv1beta1.NotificationPolicy, summary *libsveltosv1beta1.NotificationSummary,
	state clusterState, lastDelivered bool) bool {

	if !lastDelivered {
		// Nothing was ever delivered. A cluster that is passing has nothing to report.
		return state.failing
	}

	if *summary.LastSentFailing != state.failing {
		return true
	}

	// Still failing: report a change of the failure message, unless only transitions are of interest
	return state.failing && !policy.OnlyOnTransition && summary.LastSentMessageHash != state.messageHash
}

// notificationRequeueAfter returns after how long the ClusterHealthCheck has to be reconciled again
// because a notification is held back by its policy (MinInterval or FailingFor).
// Zero if there is nothing pending.
func notificationRequeueAfter(chc *libsveltosv1beta1.ClusterHealthCheck, now time.Time) time.Duration {
	var requeueAfter time.Duration

	for i := range chc.Status.ClusterConditions {
		cc := &chc.Status.ClusterConditions[i]
		if cc.ClusterInfo.Status != libsveltosv1beta1.SveltosStatusProvisioned {
			continue
		}

		state := getClusterState(cc.Conditions)
		for j := range chc.Spec.Notifications {
			n := &chc.Spec.Notifications[j]
			if !hasNotificationPolicy(n) {
				continue
			}

			summary := getNotificationSummary(cc, n.Name)
			send, wait := evaluateNotificationPolicy(n.Policy, summary, state, now)
			if !send && wait > 0 && (requeueAfter == 0 || wait < requeueAfter) {
				requeueAfter = wait
			}
		}
	}

	return requeueAfter
}

// getNotificationSummary returns the summary of the notification in the given cluster condition. Nil if none.
func getNotificationSummary(cc *libsveltosv1beta1.ClusterCondition, name string) *libsveltosv1beta1.NotificationSummary {
	for i := range cc.NotificationSummaries {
		if cc.NotificationSummaries[i].Name == name {
			return &cc.NotificationSummaries[i]
		}
	}

	return nil
}
