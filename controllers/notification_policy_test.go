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
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/events"
	"k8s.io/klog/v2/textlogger"

	"github.com/projectsveltos/healthcheck-manager/controllers"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
)

const (
	hash1 = "hash1"
	hash2 = "hash2"

	notificationName = "n"
	checkOne         = "check1"
	checkTwo         = "check2"
	checkThree       = "check3"
	singleCheck      = "check"
	plainNotif       = "plain"
	capiClusterKind  = "Cluster"
	capiAPIVersion   = "cluster.x-k8s.io/v1beta2"
)

func duration(d time.Duration) *metav1.Duration {
	return &metav1.Duration{Duration: d}
}

// summaryDelivered returns the summary of a notification delivered at the given time
func summaryDelivered(sentAt time.Time, failing bool, messageHash string) *libsveltosv1beta1.NotificationSummary {
	sent := metav1.Time{Time: sentAt}
	return &libsveltosv1beta1.NotificationSummary{
		Name:                notificationName,
		Status:              libsveltosv1beta1.NotificationStatusDelivered,
		LastSentTime:        &sent,
		LastSentFailing:     &failing,
		LastSentMessageHash: messageHash,
	}
}

var _ = Describe("Notification policy", func() {
	var now time.Time

	BeforeEach(func() {
		now = time.Now()
	})

	Context("hasNotificationPolicy", func() {
		It("is false without policy or with a policy that sets nothing", func() {
			Expect(controllers.HasNotificationPolicy(&libsveltosv1beta1.Notification{})).To(BeFalse())
			Expect(controllers.HasNotificationPolicy(&libsveltosv1beta1.Notification{
				Policy: &libsveltosv1beta1.NotificationPolicy{},
			})).To(BeFalse())
			Expect(controllers.HasNotificationPolicy(&libsveltosv1beta1.Notification{
				Policy: &libsveltosv1beta1.NotificationPolicy{MinInterval: duration(0), FailingFor: duration(0)},
			})).To(BeFalse())
		})

		It("is true as soon as one setting is set", func() {
			Expect(controllers.HasNotificationPolicy(&libsveltosv1beta1.Notification{
				Policy: &libsveltosv1beta1.NotificationPolicy{OnlyOnTransition: true},
			})).To(BeTrue())
			Expect(controllers.HasNotificationPolicy(&libsveltosv1beta1.Notification{
				Policy: &libsveltosv1beta1.NotificationPolicy{MinInterval: duration(time.Minute)},
			})).To(BeTrue())
			Expect(controllers.HasNotificationPolicy(&libsveltosv1beta1.Notification{
				Policy: &libsveltosv1beta1.NotificationPolicy{FailingFor: duration(time.Minute)},
			})).To(BeTrue())
		})
	})

	Context("getClusterState", func() {
		It("is not failing when all conditions are passing", func() {
			state := controllers.GetClusterState([]libsveltosv1beta1.Condition{
				{Type: checkOne, Status: corev1.ConditionTrue},
				{Type: checkTwo, Status: corev1.ConditionTrue},
			})
			Expect(state.Failing()).To(BeFalse())
			Expect(state.MessageHash()).To(BeEmpty())
		})

		It("is failing when at least a condition is not passing", func() {
			state := controllers.GetClusterState([]libsveltosv1beta1.Condition{
				{Type: checkOne, Status: corev1.ConditionTrue},
				{Type: checkTwo, Status: corev1.ConditionFalse, Message: "failure"},
			})
			Expect(state.Failing()).To(BeTrue())
			Expect(state.MessageHash()).ToNot(BeEmpty())
		})

		It("has the same hash for the same failures and a different one when the failure message changes", func() {
			failing := func(message string) []libsveltosv1beta1.Condition {
				return []libsveltosv1beta1.Condition{{Type: checkOne, Status: corev1.ConditionFalse, Message: message}}
			}
			Expect(controllers.GetClusterState(failing("a")).MessageHash()).
				To(Equal(controllers.GetClusterState(failing("a")).MessageHash()))
			Expect(controllers.GetClusterState(failing("a")).MessageHash()).
				ToNot(Equal(controllers.GetClusterState(failing("b")).MessageHash()))
		})

		It("reports the time the oldest failing check started to fail", func() {
			older := metav1.Time{Time: now.Add(-time.Hour)}
			newer := metav1.Time{Time: now.Add(-time.Minute)}
			state := controllers.GetClusterState([]libsveltosv1beta1.Condition{
				{Type: checkOne, Status: corev1.ConditionFalse, LastTransitionTime: newer},
				{Type: checkTwo, Status: corev1.ConditionFalse, LastTransitionTime: older},
				{Type: checkThree, Status: corev1.ConditionTrue, LastTransitionTime: metav1.Time{Time: now.Add(-24 * time.Hour)}},
			})
			Expect(state.FailingSince().Equal(older.Time)).To(BeTrue())
		})
	})

	Context("onlyOnTransition", func() {
		var policy *libsveltosv1beta1.NotificationPolicy

		BeforeEach(func() {
			policy = &libsveltosv1beta1.NotificationPolicy{OnlyOnTransition: true}
		})

		It("does not deliver when the cluster is found passing and nothing was ever delivered", func() {
			send, wait := controllers.EvaluateNotificationPolicy(policy, nil, controllers.NewClusterState(false, "", time.Time{}), now)
			Expect(send).To(BeFalse())
			Expect(wait).To(BeZero())
		})

		It("delivers when the cluster is found failing and nothing was ever delivered", func() {
			send, _ := controllers.EvaluateNotificationPolicy(policy, nil, controllers.NewClusterState(true, hash1, now), now)
			Expect(send).To(BeTrue())
		})

		It("delivers when a passing cluster starts failing", func() {
			summary := summaryDelivered(now.Add(-time.Hour), false, "")
			send, _ := controllers.EvaluateNotificationPolicy(policy, summary, controllers.NewClusterState(true, hash1, now), now)
			Expect(send).To(BeTrue())
		})

		It("delivers when a failing cluster recovers", func() {
			summary := summaryDelivered(now.Add(-time.Hour), true, hash1)
			send, _ := controllers.EvaluateNotificationPolicy(policy, summary, controllers.NewClusterState(false, "", time.Time{}), now)
			Expect(send).To(BeTrue())
		})

		It("does not deliver when the failure message changes and the cluster keeps failing", func() {
			summary := summaryDelivered(now.Add(-time.Hour), true, hash1)
			send, wait := controllers.EvaluateNotificationPolicy(policy, summary,
				controllers.NewClusterState(true, hash2, now.Add(-2*time.Hour)), now)
			Expect(send).To(BeFalse())
			Expect(wait).To(BeZero())
		})

		It("does not deliver when nothing changed", func() {
			summary := summaryDelivered(now.Add(-time.Hour), true, hash1)
			send, _ := controllers.EvaluateNotificationPolicy(policy, summary,
				controllers.NewClusterState(true, hash1, now.Add(-2*time.Hour)), now)
			Expect(send).To(BeFalse())
		})

		It("delivers a change of the failure message when onlyOnTransition is not set", func() {
			// Another setting is needed for the policy to apply
			policy = &libsveltosv1beta1.NotificationPolicy{MinInterval: duration(time.Minute)}
			summary := summaryDelivered(now.Add(-time.Hour), true, hash1)
			send, _ := controllers.EvaluateNotificationPolicy(policy, summary,
				controllers.NewClusterState(true, hash2, now.Add(-2*time.Hour)), now)
			Expect(send).To(BeTrue())
		})
	})

	Context("minInterval", func() {
		var policy *libsveltosv1beta1.NotificationPolicy

		BeforeEach(func() {
			policy = &libsveltosv1beta1.NotificationPolicy{MinInterval: duration(time.Minute)}
		})

		It("holds back a change inside the interval and says when to evaluate again", func() {
			summary := summaryDelivered(now.Add(-10*time.Second), true, hash1)
			send, wait := controllers.EvaluateNotificationPolicy(policy, summary,
				controllers.NewClusterState(true, hash2, now.Add(-time.Hour)), now)
			Expect(send).To(BeFalse())
			Expect(wait).To(Equal(50 * time.Second))
		})

		It("holds back a recovery inside the interval", func() {
			summary := summaryDelivered(now.Add(-10*time.Second), true, hash1)
			send, wait := controllers.EvaluateNotificationPolicy(policy, summary,
				controllers.NewClusterState(false, "", time.Time{}), now)
			Expect(send).To(BeFalse())
			Expect(wait).To(Equal(50 * time.Second))
		})

		It("delivers the change once the interval is over", func() {
			summary := summaryDelivered(now.Add(-2*time.Minute), true, hash1)
			send, wait := controllers.EvaluateNotificationPolicy(policy, summary,
				controllers.NewClusterState(true, hash2, now.Add(-time.Hour)), now)
			Expect(send).To(BeTrue())
			Expect(wait).To(BeZero())
		})

		It("has nothing to deliver, and nothing to wait for, when the state is back to what was delivered", func() {
			// failing -> passing -> failing again, all inside the interval: nothing to report
			summary := summaryDelivered(now.Add(-10*time.Second), true, hash1)
			send, wait := controllers.EvaluateNotificationPolicy(policy, summary,
				controllers.NewClusterState(true, hash1, now.Add(-time.Second)), now)
			Expect(send).To(BeFalse())
			Expect(wait).To(BeZero())
		})

		It("retries a failed delivery without waiting for the interval", func() {
			summary := summaryDelivered(now.Add(-10*time.Second), true, hash1)
			summary.Status = libsveltosv1beta1.NotificationStatusFailedToDeliver
			send, _ := controllers.EvaluateNotificationPolicy(policy, summary,
				controllers.NewClusterState(true, hash2, now.Add(-time.Hour)), now)
			Expect(send).To(BeTrue())
		})

		It("does not apply to the first delivery", func() {
			send, _ := controllers.EvaluateNotificationPolicy(policy, nil, controllers.NewClusterState(true, hash1, now), now)
			Expect(send).To(BeTrue())
		})
	})

	Context("failingFor", func() {
		var policy *libsveltosv1beta1.NotificationPolicy

		BeforeEach(func() {
			policy = &libsveltosv1beta1.NotificationPolicy{FailingFor: duration(time.Minute)}
		})

		It("holds back a failure that did not last long enough and says when to evaluate again", func() {
			send, wait := controllers.EvaluateNotificationPolicy(policy, nil,
				controllers.NewClusterState(true, hash1, now.Add(-10*time.Second)), now)
			Expect(send).To(BeFalse())
			Expect(wait).To(Equal(50 * time.Second))
		})

		It("delivers a failure that lasted long enough", func() {
			send, wait := controllers.EvaluateNotificationPolicy(policy, nil,
				controllers.NewClusterState(true, hash1, now.Add(-2*time.Minute)), now)
			Expect(send).To(BeTrue())
			Expect(wait).To(BeZero())
		})

		It("never delivers a failure that ended before it lasted long enough, nor its recovery", func() {
			send, wait := controllers.EvaluateNotificationPolicy(policy, nil,
				controllers.NewClusterState(false, "", time.Time{}), now)
			Expect(send).To(BeFalse())
			Expect(wait).To(BeZero())
		})

		It("holds back a new failure after the cluster recovered", func() {
			summary := summaryDelivered(now.Add(-time.Hour), false, "")
			send, wait := controllers.EvaluateNotificationPolicy(policy, summary,
				controllers.NewClusterState(true, hash1, now.Add(-20*time.Second)), now)
			Expect(send).To(BeFalse())
			Expect(wait).To(Equal(40 * time.Second))
		})

		It("delivers a recovery without waiting", func() {
			summary := summaryDelivered(now.Add(-time.Hour), true, hash1)
			send, _ := controllers.EvaluateNotificationPolicy(policy, summary,
				controllers.NewClusterState(false, "", time.Time{}), now)
			Expect(send).To(BeTrue())
		})

		It("does not hold back a change of the message of a failure already delivered", func() {
			summary := summaryDelivered(now.Add(-time.Hour), true, hash1)
			send, _ := controllers.EvaluateNotificationPolicy(policy, summary,
				controllers.NewClusterState(true, hash2, now.Add(-time.Hour)), now)
			Expect(send).To(BeTrue())
		})
	})

	Context("failingFor and minInterval together", func() {
		It("first waits for the failure to last, then for the interval since the last delivery", func() {
			policy := &libsveltosv1beta1.NotificationPolicy{
				FailingFor:  duration(time.Minute),
				MinInterval: duration(10 * time.Minute),
			}
			// Recovery delivered 2 minutes ago, cluster failing for 90 seconds
			summary := summaryDelivered(now.Add(-2*time.Minute), false, "")
			send, wait := controllers.EvaluateNotificationPolicy(policy, summary,
				controllers.NewClusterState(true, hash1, now.Add(-90*time.Second)), now)
			Expect(send).To(BeFalse())
			Expect(wait).To(Equal(8 * time.Minute))
		})
	})

	Context("notificationRequeueAfter", func() {
		var chc *libsveltosv1beta1.ClusterHealthCheck

		BeforeEach(func() {
			chc = &libsveltosv1beta1.ClusterHealthCheck{
				Spec: libsveltosv1beta1.ClusterHealthCheckSpec{
					Notifications: []libsveltosv1beta1.Notification{
						{Name: "held", Type: libsveltosv1beta1.NotificationTypeKubernetesEvent,
							Policy: &libsveltosv1beta1.NotificationPolicy{FailingFor: duration(time.Minute)}},
						{Name: plainNotif, Type: libsveltosv1beta1.NotificationTypeKubernetesEvent},
					},
				},
				Status: libsveltosv1beta1.ClusterHealthCheckStatus{
					ClusterConditions: []libsveltosv1beta1.ClusterCondition{{
						ClusterInfo: libsveltosv1beta1.ClusterInfo{Status: libsveltosv1beta1.SveltosStatusProvisioned},
						Conditions: []libsveltosv1beta1.Condition{{
							Type: singleCheck, Status: corev1.ConditionFalse,
							LastTransitionTime: metav1.Time{Time: time.Now().Add(-20 * time.Second)},
						}},
					}},
				},
			}
		})

		It("returns the time left of a held back notification", func() {
			wait := controllers.NotificationRequeueAfter(chc, time.Now())
			Expect(wait).To(BeNumerically("~", 40*time.Second, 2*time.Second))
		})

		It("returns zero when the notification has no policy", func() {
			chc.Spec.Notifications = chc.Spec.Notifications[1:]
			Expect(controllers.NotificationRequeueAfter(chc, time.Now())).To(BeZero())
		})

		It("returns zero when the cluster is passing", func() {
			chc.Status.ClusterConditions[0].Conditions[0].Status = corev1.ConditionTrue
			Expect(controllers.NotificationRequeueAfter(chc, time.Now())).To(BeZero())
		})

		It("ignores a cluster that is not Provisioned", func() {
			chc.Status.ClusterConditions[0].ClusterInfo.Status = libsveltosv1beta1.SveltosStatusProvisioning
			Expect(controllers.NotificationRequeueAfter(chc, time.Now())).To(BeZero())
		})

		It("returns the shortest wait", func() {
			chc.Spec.Notifications = append(chc.Spec.Notifications, libsveltosv1beta1.Notification{
				Name: "shorter", Type: libsveltosv1beta1.NotificationTypeKubernetesEvent,
				Policy: &libsveltosv1beta1.NotificationPolicy{FailingFor: duration(30 * time.Second)},
			})
			wait := controllers.NotificationRequeueAfter(chc, time.Now())
			Expect(wait).To(BeNumerically("~", 10*time.Second, 2*time.Second))
		})
	})

	Context("sendNotifications", func() {
		var recorder *events.FakeRecorder
		var chc *libsveltosv1beta1.ClusterHealthCheck
		var clusterNamespace, clusterName string
		var clusterType libsveltosv1beta1.ClusterType

		failing := func(message string, since time.Time) []libsveltosv1beta1.Condition {
			return []libsveltosv1beta1.Condition{{
				Type: singleCheck, Status: corev1.ConditionFalse, Message: message,
				LastTransitionTime: metav1.Time{Time: since},
			}}
		}
		passing := []libsveltosv1beta1.Condition{{Type: singleCheck, Status: corev1.ConditionTrue}}

		// evaluate runs sendNotifications and records the result in the status, as the controller does
		evaluate := func(conditions []libsveltosv1beta1.Condition) {
			summaries, _, err := controllers.SendNotifications(context.TODO(), nil, clusterNamespace, clusterName,
				clusterType, chc, false, conditions, textlogger.NewLogger(textlogger.NewConfig()))
			Expect(err).To(BeNil())
			chc.Status.ClusterConditions[0].Conditions = conditions
			chc.Status.ClusterConditions[0].NotificationSummaries = summaries
		}

		sentEvents := func() int {
			return len(recorder.Events)
		}

		BeforeEach(func() {
			recorder = events.NewFakeRecorder(100)
			controllers.SetManagementRecorder(recorder)

			clusterNamespace = randomString()
			clusterName = randomString()
			clusterType = libsveltosv1beta1.ClusterTypeCapi

			chc = &libsveltosv1beta1.ClusterHealthCheck{
				ObjectMeta: metav1.ObjectMeta{Name: randomString()},
				Status: libsveltosv1beta1.ClusterHealthCheckStatus{
					ClusterConditions: []libsveltosv1beta1.ClusterCondition{{
						ClusterInfo: libsveltosv1beta1.ClusterInfo{
							Cluster: corev1.ObjectReference{
								Namespace: clusterNamespace, Name: clusterName,
								Kind: capiClusterKind, APIVersion: capiAPIVersion,
							},
							Status: libsveltosv1beta1.SveltosStatusProvisioned,
						},
					}},
				},
			}
		})

		AfterEach(func() {
			controllers.SetManagementRecorder(nil)
		})

		It("onlyOnTransition: delivers the failure and the recovery, not the changes in between", func() {
			chc.Spec.Notifications = []libsveltosv1beta1.Notification{{
				Name: notificationName, Type: libsveltosv1beta1.NotificationTypeKubernetesEvent,
				Policy: &libsveltosv1beta1.NotificationPolicy{OnlyOnTransition: true},
			}}
			since := time.Now().Add(-time.Hour)

			evaluate(passing)
			Expect(sentEvents()).To(Equal(0)) // found passing: nothing to report

			evaluate(failing("deployment a is degraded", since))
			Expect(sentEvents()).To(Equal(1))

			evaluate(failing("deployment a is degraded  \ndeployment b is degraded", since))
			evaluate(failing("deployment b is degraded", since))
			Expect(sentEvents()).To(Equal(1)) // message changed while failing: not delivered

			evaluate(passing)
			Expect(sentEvents()).To(Equal(2)) // recovery
		})

		It("minInterval: coalesces changes inside the interval", func() {
			chc.Spec.Notifications = []libsveltosv1beta1.Notification{{
				Name: notificationName, Type: libsveltosv1beta1.NotificationTypeKubernetesEvent,
				Policy: &libsveltosv1beta1.NotificationPolicy{MinInterval: duration(time.Hour)},
			}}
			since := time.Now().Add(-time.Minute)

			evaluate(failing("a", since))
			Expect(sentEvents()).To(Equal(1))

			evaluate(failing("a  \nb", since))
			evaluate(passing)
			Expect(sentEvents()).To(Equal(1)) // inside the interval

			// The interval is over: the current state is delivered
			sentAt := metav1.Time{Time: time.Now().Add(-2 * time.Hour)}
			chc.Status.ClusterConditions[0].NotificationSummaries[0].LastSentTime = &sentAt
			evaluate(passing)
			Expect(sentEvents()).To(Equal(2))

			// Nothing changed since
			evaluate(passing)
			Expect(sentEvents()).To(Equal(2))
		})

		It("failingFor: a failure that does not last long enough is never delivered, nor its recovery", func() {
			chc.Spec.Notifications = []libsveltosv1beta1.Notification{{
				Name: notificationName, Type: libsveltosv1beta1.NotificationTypeKubernetesEvent,
				Policy: &libsveltosv1beta1.NotificationPolicy{FailingFor: duration(time.Hour)},
			}}

			evaluate(failing("a", time.Now().Add(-time.Minute)))
			Expect(sentEvents()).To(Equal(0))

			evaluate(passing)
			Expect(sentEvents()).To(Equal(0))
		})

		It("failingFor: delivers the failure once it lasted long enough, and its recovery", func() {
			chc.Spec.Notifications = []libsveltosv1beta1.Notification{{
				Name: notificationName, Type: libsveltosv1beta1.NotificationTypeKubernetesEvent,
				Policy: &libsveltosv1beta1.NotificationPolicy{FailingFor: duration(time.Hour)},
			}}

			evaluate(failing("a", time.Now().Add(-2*time.Hour)))
			Expect(sentEvents()).To(Equal(1))

			evaluate(passing)
			Expect(sentEvents()).To(Equal(2))
		})

		It("keeps the legacy behavior for a notification without policy", func() {
			chc.Spec.Notifications = []libsveltosv1beta1.Notification{{
				Name: notificationName, Type: libsveltosv1beta1.NotificationTypeKubernetesEvent,
			}}
			since := time.Now().Add(-time.Hour)

			// resendAll=true means a liveness check changed: always delivered
			for i := 1; i <= 3; i++ {
				summaries, _, err := controllers.SendNotifications(context.TODO(), nil, clusterNamespace, clusterName,
					clusterType, chc, true, failing("a", since), textlogger.NewLogger(textlogger.NewConfig()))
				Expect(err).To(BeNil())
				chc.Status.ClusterConditions[0].NotificationSummaries = summaries
				Expect(sentEvents()).To(Equal(i))
			}

			// resendAll=false and already delivered: not delivered again
			evaluate(failing("a", since))
			Expect(sentEvents()).To(Equal(3))
		})

		It("the policy of a notification does not affect another notification", func() {
			chc.Spec.Notifications = []libsveltosv1beta1.Notification{
				{Name: "quiet", Type: libsveltosv1beta1.NotificationTypeKubernetesEvent,
					Policy: &libsveltosv1beta1.NotificationPolicy{OnlyOnTransition: true}},
				{Name: plainNotif, Type: libsveltosv1beta1.NotificationTypeKubernetesEvent},
			}
			since := time.Now().Add(-time.Hour)

			// First evaluation, nothing delivered before: both deliver the failure
			evaluate(failing("a", since))
			Expect(sentEvents()).To(Equal(2))

			// The plain one is told a check changed (resendAll), the quiet one ignores the message change
			summaries, _, err := controllers.SendNotifications(context.TODO(), nil, clusterNamespace, clusterName,
				clusterType, chc, true, failing("a  \nb", since), textlogger.NewLogger(textlogger.NewConfig()))
			Expect(err).To(BeNil())
			Expect(summaries).To(HaveLen(2))
			Expect(sentEvents()).To(Equal(3)) // only the plain one
		})

		It("records what was delivered, and keeps it when a notification is held back", func() {
			chc.Spec.Notifications = []libsveltosv1beta1.Notification{{
				Name: notificationName, Type: libsveltosv1beta1.NotificationTypeKubernetesEvent,
				Policy: &libsveltosv1beta1.NotificationPolicy{OnlyOnTransition: true},
			}}
			since := time.Now().Add(-time.Hour)

			evaluate(failing("a", since))
			summary := chc.Status.ClusterConditions[0].NotificationSummaries[0]
			Expect(summary.Status).To(Equal(libsveltosv1beta1.NotificationStatusDelivered))
			Expect(summary.LastSentTime).ToNot(BeNil())
			Expect(summary.LastSentFailing).ToNot(BeNil())
			Expect(*summary.LastSentFailing).To(BeTrue())
			Expect(summary.LastSentMessageHash).ToNot(BeEmpty())

			// Held back: the record of the last delivery stays the same
			evaluate(failing("a  \nb", since))
			held := chc.Status.ClusterConditions[0].NotificationSummaries[0]
			Expect(held.LastSentTime.Equal(summary.LastSentTime)).To(BeTrue())
			Expect(held.LastSentMessageHash).To(Equal(summary.LastSentMessageHash))
		})
	})

	Context("conditions: stable output", func() {
		It("isStatusHealthy returns the same message whatever the order of the resources", func() {
			resource := func(name string) libsveltosv1beta1.ResourceStatus {
				return libsveltosv1beta1.ResourceStatus{
					ObjectRef:    corev1.ObjectReference{Kind: "Deployment", Namespace: "default", Name: name},
					HealthStatus: libsveltosv1beta1.HealthStatusDegraded,
				}
			}
			report := func(names ...string) *libsveltosv1beta1.HealthCheckReport {
				r := &libsveltosv1beta1.HealthCheckReport{}
				for i := range names {
					r.Spec.ResourceStatuses = append(r.Spec.ResourceStatuses, resource(names[i]))
				}
				return r
			}

			message1, healthy1 := controllers.IsStatusHealthy(report("a", "b", "c"))
			message2, healthy2 := controllers.IsStatusHealthy(report("c", "a", "b"))
			Expect(healthy1).To(BeFalse())
			Expect(healthy2).To(BeFalse())
			Expect(message1).To(Equal(message2))
		})

		It("getPreviousCondition returns the condition recorded for the liveness check in the cluster", func() {
			clusterNamespace := randomString()
			clusterName := randomString()
			livenessCheck := &libsveltosv1beta1.LivenessCheck{Name: "check", Type: libsveltosv1beta1.LivenessTypeAddons}

			chc := &libsveltosv1beta1.ClusterHealthCheck{
				Status: libsveltosv1beta1.ClusterHealthCheckStatus{
					ClusterConditions: []libsveltosv1beta1.ClusterCondition{{
						ClusterInfo: libsveltosv1beta1.ClusterInfo{Cluster: corev1.ObjectReference{
							Namespace: clusterNamespace, Name: clusterName,
							Kind: capiClusterKind, APIVersion: capiAPIVersion,
						}},
						Conditions: []libsveltosv1beta1.Condition{{
							Name: livenessCheck.Name, Type: libsveltosv1beta1.ConditionType(controllers.GetConditionType(livenessCheck)),
							Status: corev1.ConditionFalse,
						}},
					}},
				},
			}

			previous := controllers.GetPreviousCondition(chc, clusterNamespace, clusterName,
				libsveltosv1beta1.ClusterTypeCapi, livenessCheck)
			Expect(previous).ToNot(BeNil())
			Expect(previous.Status).To(Equal(corev1.ConditionFalse))

			Expect(controllers.GetPreviousCondition(chc, clusterNamespace, randomString(),
				libsveltosv1beta1.ClusterTypeCapi, livenessCheck)).To(BeNil())
		})
	})
})
