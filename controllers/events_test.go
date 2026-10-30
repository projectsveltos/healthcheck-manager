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

package controllers_test

import (
	"context"
	"errors"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/projectsveltos/healthcheck-manager/controllers"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
)

const (
	eventBufferSize      = 10
	testNotificationName = "ops-channel"

	cleanupInProgress     = "cleanup is in progress"
	agentCleanupQueued    = "agent cleanup request is queued"
	agentRemovingResource = "agent is removing ClusterHealthCheck resources"
)

var _ = Describe("ClusterHealthCheck events", func() {
	var recorder *events.FakeRecorder
	var chc *libsveltosv1beta1.ClusterHealthCheck
	var cluster *corev1.ObjectReference

	BeforeEach(func() {
		recorder = events.NewFakeRecorder(eventBufferSize)
		controllers.SetManagementRecorder(recorder)

		// A name which is new for each spec: what was recorded for a ClusterHealthCheck is remembered
		chc = &libsveltosv1beta1.ClusterHealthCheck{ObjectMeta: metav1.ObjectMeta{Name: randomString()}}
		cluster = &corev1.ObjectReference{
			Kind:       libsveltosv1beta1.SveltosClusterKind,
			APIVersion: libsveltosv1beta1.GroupVersion.String(),
			Namespace:  randomString(),
			Name:       randomString(),
		}
	})

	AfterEach(func() {
		controllers.SetManagementRecorder(nil)
	})

	Context("deploying a ClusterHealthCheck to a cluster", func() {
		// record calls what the deployer calls before it saves the ClusterInfo of a cluster
		record := func(cluster *corev1.ObjectReference, status libsveltosv1beta1.SveltosFeatureStatus, failureMessage *string) {
			c := fake.NewClientBuilder().WithScheme(scheme).Build()
			clusterInfo := &libsveltosv1beta1.ClusterInfo{Cluster: *cluster, Status: status, FailureMessage: failureMessage}
			controllers.RecordClusterInfoFailure(context.TODO(), c, chc, clusterInfo)
		}

		message := func(m string) *string { return &m }

		clusterNote := func(cluster *corev1.ObjectReference) string {
			return "Sveltos " + cluster.Namespace + "/" + cluster.Name
		}

		It("records ClusterHealthCheckFailed once, and again when the failure message changes", func() {
			record(cluster, libsveltosv1beta1.SveltosStatusProvisioning, message("failed resetting HealthCheckReport status"))
			Expect(recorder.Events).To(Receive(Equal("Warning ClusterHealthCheckFailed Deploying to cluster " +
				clusterNote(cluster) + " failed: failed resetting HealthCheckReport status")))

			// The same failure on the next attempt: not new
			record(cluster, libsveltosv1beta1.SveltosStatusFailed, message("failed resetting HealthCheckReport status"))
			Expect(recorder.Events).To(BeEmpty())

			// A different failure
			record(cluster, libsveltosv1beta1.SveltosStatusFailed, message("HealthCheck not found"))
			Expect(recorder.Events).To(Receive(Equal("Warning ClusterHealthCheckFailed Deploying to cluster " +
				clusterNote(cluster) + " failed: HealthCheck not found")))
			Expect(recorder.Events).To(BeEmpty())
		})

		It("does not record a failure which repeats after the deployment was queued again", func() {
			// A retry is Failed, then Provisioning without a failure message (the deployment is queued
			// again), then Failed with the same message
			record(cluster, libsveltosv1beta1.SveltosStatusFailed, message("failed to deploy"))
			Expect(recorder.Events).To(Receive(ContainSubstring("failed to deploy")))

			for range 3 {
				record(cluster, libsveltosv1beta1.SveltosStatusProvisioning, nil)
				record(cluster, libsveltosv1beta1.SveltosStatusFailed, message("failed to deploy"))
			}
			Expect(recorder.Events).To(BeEmpty())
		})

		It("records a failure again after the ClusterHealthCheck was deployed", func() {
			record(cluster, libsveltosv1beta1.SveltosStatusFailed, message("failed to deploy"))
			Expect(recorder.Events).To(Receive(ContainSubstring("failed to deploy")))

			record(cluster, libsveltosv1beta1.SveltosStatusProvisioned, nil)

			record(cluster, libsveltosv1beta1.SveltosStatusFailed, message("failed to deploy"))
			Expect(recorder.Events).To(Receive(ContainSubstring("failed to deploy")))
		})

		It("records the failure of each cluster, with the cluster as the related object", func() {
			otherCluster := cluster.DeepCopy()
			otherCluster.Name = randomString()

			record(cluster, libsveltosv1beta1.SveltosStatusFailed, message("failed to deploy"))
			record(otherCluster, libsveltosv1beta1.SveltosStatusFailed, message("failed to deploy"))

			Expect(recorder.Events).To(Receive(ContainSubstring("Deploying to cluster " + clusterNote(cluster))))
			Expect(recorder.Events).To(Receive(ContainSubstring("Deploying to cluster " + clusterNote(otherCluster))))
		})

		It("records a failure while the ClusterHealthCheck is removed with the action Removing", func() {
			recorder.Verbose = true

			record(cluster, libsveltosv1beta1.SveltosStatusRemoving, message("failed to remove HealthCheck"))
			Expect(recorder.Events).To(Receive(And(
				ContainSubstring("ClusterHealthCheckFailed Removing"),
				ContainSubstring("Removing from cluster "+clusterNote(cluster)+" failed: failed to remove HealthCheck"))))
		})

		It("records nothing for a cluster which is paused, not ready or down, whose agent is not healthy, "+
			"or while a deployment or a cleanup is in progress", func() {

			for _, m := range []string{
				"cannot proceed deploying. Either cluster is paused or not ready.",
				"agent in managed cluster is not healthy.",
				cleanupInProgress,
				"provisioning is in progress",
				agentCleanupQueued,
				agentRemovingResource,
			} {
				record(cluster, libsveltosv1beta1.SveltosStatusProvisioning, message(m))
			}
			Expect(recorder.Events).To(BeEmpty())
		})

		It("records nothing when there is no failure", func() {
			record(cluster, libsveltosv1beta1.SveltosStatusProvisioned, message(""))
			record(cluster, libsveltosv1beta1.SveltosStatusProvisioned, nil)
			record(cluster, libsveltosv1beta1.SveltosStatusRemoved, nil)
			Expect(recorder.Events).To(BeEmpty())
		})

		It("does not fail when no recorder is set", func() {
			controllers.SetManagementRecorder(nil)
			Expect(func() {
				record(cluster, libsveltosv1beta1.SveltosStatusFailed, message("failed to deploy"))
			}).ToNot(Panic())
		})
	})

	Context("delivering a notification", func() {
		var notification *libsveltosv1beta1.Notification

		BeforeEach(func() {
			notification = &libsveltosv1beta1.Notification{Name: testNotificationName, Type: libsveltosv1beta1.NotificationTypeSlack}
		})

		summary := func(status libsveltosv1beta1.NotificationStatus, failureMessage *string) *libsveltosv1beta1.NotificationSummary {
			return &libsveltosv1beta1.NotificationSummary{Name: testNotificationName, Status: status, FailureMessage: failureMessage}
		}

		// record calls what sendNotifications calls for a notification: previous is what the ClusterHealthCheck
		// recorded before this evaluation
		record := func(previous, current *libsveltosv1beta1.NotificationSummary) {
			c := fake.NewClientBuilder().WithScheme(scheme).Build()
			controllers.RecordNotificationFailure(context.TODO(), c, chc, cluster.Namespace, cluster.Name,
				libsveltosv1beta1.ClusterTypeSveltos, notification, previous, current)
		}

		message := func(m string) *string { return &m }

		It("records NotificationFailed once, and again when the failure message changes", func() {
			failed := libsveltosv1beta1.NotificationStatusFailedToDeliver

			record(nil, summary(failed, message("invalid_auth")))
			Expect(recorder.Events).To(Receive(Equal("Warning NotificationFailed Delivering notification ops-channel (Slack) " +
				"for cluster Sveltos " + cluster.Namespace + "/" + cluster.Name + " failed: invalid_auth")))

			// The notification is delivered again and fails the same way: not new
			record(summary(failed, message("invalid_auth")), summary(failed, message("invalid_auth")))
			Expect(recorder.Events).To(BeEmpty())

			// A different failure
			record(summary(failed, message("invalid_auth")), summary(failed, message("channel_not_found")))
			Expect(recorder.Events).To(Receive(ContainSubstring("failed: channel_not_found")))
			Expect(recorder.Events).To(BeEmpty())
		})

		It("records a failure after the notification was delivered", func() {
			record(summary(libsveltosv1beta1.NotificationStatusDelivered, nil),
				summary(libsveltosv1beta1.NotificationStatusFailedToDeliver, message("invalid_auth")))
			Expect(recorder.Events).To(Receive(ContainSubstring("failed: invalid_auth")))
		})

		It("records nothing when the notification is delivered", func() {
			record(nil, summary(libsveltosv1beta1.NotificationStatusDelivered, nil))
			record(summary(libsveltosv1beta1.NotificationStatusFailedToDeliver, message("invalid_auth")),
				summary(libsveltosv1beta1.NotificationStatusDelivered, nil))
			Expect(recorder.Events).To(BeEmpty())
		})
	})
})

var _ = Describe("errors only saying a request is still waiting", func() {
	waiting := []string{
		"request to deploy queued",
		"cleanup request is queued",
		"clusterHealthCheck is still being provisioned",
		"request to process ClusterHealthCheck is still queued in one ore more clusters",
		cleanupInProgress,
		agentCleanupQueued,
		agentRemovingResource,
	}

	It("isWaiting is true for a request which has not completed yet", func() {
		for _, m := range waiting {
			Expect(controllers.IsWaiting(errors.New(m))).To(BeTrue(), m)
		}
	})

	It("isWaiting is false for a failure", func() {
		Expect(controllers.IsWaiting(errors.New("failed to evaluate liveness checks"))).To(BeFalse())
	})

	It("keepFailure reports the last error when it is a failure", func() {
		first := errors.New("first failure")
		last := errors.New("last failure")
		Expect(controllers.KeepFailure(nil, last)).To(Equal(last))
		Expect(controllers.KeepFailure(first, last)).To(Equal(last))
		Expect(controllers.KeepFailure(errors.New(waiting[0]), last)).To(Equal(last))
	})

	It("keepFailure does not replace an error with one only saying a request is still waiting", func() {
		failure := errors.New("failed to evaluate liveness checks")
		Expect(controllers.KeepFailure(failure, errors.New(waiting[0]))).To(Equal(failure))

		stillWaiting := errors.New(waiting[1])
		Expect(controllers.KeepFailure(nil, stillWaiting)).To(Equal(stillWaiting))
	})
})
