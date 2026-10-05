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

package fv_test

import (
	"context"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	eventsv1 "k8s.io/api/events/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
)

const (
	policyLabelKey = "fv-notification-policy"
	stateKey       = "state"
	detailKey      = "detail"

	stateHealthy = "healthy"
	stateBroken  = "broken"

	// Message of the notification sent when all liveness checks are passing
	healthyMessage = "All liveness checks are passing"

	defaultNamespace = "default"

	// Time during which something that must not happen is observed
	quietPeriod = 30 * time.Second
)

// A HealthCheck reporting Degraded when the "state" of the selected ConfigMap is "broken".
// The "detail" of the ConfigMap is the message, which makes every failure recognizable.
const evaluateConfigMapState = `
function evaluate()
  local statuses = {}
  for _, resource in ipairs(resources) do
    local status = "Healthy"
    local message = ""
    if resource.data ~= nil and resource.data.state == "broken" then
      status = "Degraded"
      message = resource.data.detail
    end
    table.insert(statuses, {resource=resource, status=status, message=message})
  end
  local hs = {}
  if #statuses > 0 then
    hs.resources = statuses
  end
  return hs
end`

// notificationPolicyTest drives the health of a cluster through a ConfigMap in the managed cluster and
// observes what ClusterHealthCheck delivers.
type notificationPolicyTest struct {
	healthCheck    *libsveltosv1beta1.HealthCheck
	configMap      *corev1.ConfigMap
	clusterHealth  *libsveltosv1beta1.ClusterHealthCheck
	workloadClient client.Client
}

// newNotificationPolicyTest creates a ClusterHealthCheck with a Kubernetes event notification using the
// given policy, and waits for the cluster to be evaluated as healthy.
func newNotificationPolicyTest(namePrefix string, policy *libsveltosv1beta1.NotificationPolicy) *notificationPolicyTest {
	t := &notificationPolicyTest{}

	var err error
	t.workloadClient, err = getKindWorkloadClusterKubeconfig()
	Expect(err).To(BeNil())

	labelValue := randomString()

	t.configMap = &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      namePrefix + randomString(),
			Namespace: defaultNamespace,
			Labels:    map[string]string{policyLabelKey: labelValue},
		},
		Data: map[string]string{stateKey: stateHealthy},
	}
	Byf("Creating ConfigMap %s/%s in the managed cluster", t.configMap.Namespace, t.configMap.Name)
	Expect(t.workloadClient.Create(context.TODO(), t.configMap)).To(Succeed())

	t.healthCheck = &libsveltosv1beta1.HealthCheck{
		ObjectMeta: metav1.ObjectMeta{Name: namePrefix + randomString()},
		Spec: libsveltosv1beta1.HealthCheckSpec{
			ResourceSelectors: []libsveltosv1beta1.ResourceSelector{{
				Group:   "",
				Version: apiVersionV1,
				Kind:    "ConfigMap",
				LabelFilters: []libsveltosv1beta1.LabelFilter{
					{Key: policyLabelKey, Operation: libsveltosv1beta1.OperationEqual, Value: labelValue},
				},
			}},
			EvaluateHealth: evaluateConfigMapState,
		},
	}
	Byf("Creating HealthCheck %s", t.healthCheck.Name)
	Expect(k8sClient.Create(context.TODO(), t.healthCheck)).To(Succeed())

	livenessCheck := libsveltosv1beta1.LivenessCheck{
		Name: randomString(),
		Type: libsveltosv1beta1.LivenessTypeHealthCheck,
		LivenessSourceRef: &corev1.ObjectReference{
			Name:       t.healthCheck.Name,
			APIVersion: libsveltosv1beta1.GroupVersion.String(),
			Kind:       libsveltosv1beta1.HealthCheckKind,
		},
	}
	notification := libsveltosv1beta1.Notification{
		Name:   randomString(),
		Type:   libsveltosv1beta1.NotificationTypeKubernetesEvent,
		Policy: policy,
	}

	Byf("Creating a ClusterHealthCheck matching Cluster %s/%s", kindWorkloadCluster.GetNamespace(), kindWorkloadCluster.GetName())
	t.clusterHealth = getClusterHealthCheck(namePrefix, map[string]string{key: value},
		[]libsveltosv1beta1.LivenessCheck{livenessCheck}, []libsveltosv1beta1.Notification{notification})
	Expect(k8sClient.Create(context.TODO(), t.clusterHealth)).To(Succeed())

	DeferCleanup(t.cleanup)

	Byf("Waiting for the cluster to be evaluated as healthy")
	Eventually(func() bool {
		cc := t.clusterCondition()
		return cc != nil && cc.ClusterInfo.Status == libsveltosv1beta1.SveltosStatusProvisioned &&
			len(cc.Conditions) == 1 && cc.Conditions[0].Status == corev1.ConditionTrue
	}, timeout, pollingInterval).Should(BeTrue())

	return t
}

func (t *notificationPolicyTest) cleanup() {
	Byf("Deleting ClusterHealthCheck %s", t.clusterHealth.Name)
	deleteClusterHealthCheck(t.clusterHealth.Name)

	Byf("Deleting HealthCheck %s", t.healthCheck.Name)
	Expect(client.IgnoreNotFound(k8sClient.Delete(context.TODO(), t.healthCheck))).To(Succeed())

	Byf("Deleting ConfigMap %s/%s", t.configMap.Namespace, t.configMap.Name)
	Expect(client.IgnoreNotFound(t.workloadClient.Delete(context.TODO(), t.configMap))).To(Succeed())
}

// setHealth changes the health of the managed cluster. The detail is the failure message.
func (t *notificationPolicyTest) setHealth(state, detail string) {
	Byf("Setting the state of the managed cluster to %q (detail %q)", state, detail)
	Eventually(func() error {
		current := &corev1.ConfigMap{}
		err := t.workloadClient.Get(context.TODO(),
			types.NamespacedName{Namespace: t.configMap.Namespace, Name: t.configMap.Name}, current)
		if err != nil {
			return err
		}
		current.Data = map[string]string{stateKey: state, detailKey: detail}
		return t.workloadClient.Update(context.TODO(), current)
	}, timeout, pollingInterval).Should(Succeed())
}

func (t *notificationPolicyTest) clusterCondition() *libsveltosv1beta1.ClusterCondition {
	current := &libsveltosv1beta1.ClusterHealthCheck{}
	err := k8sClient.Get(context.TODO(), types.NamespacedName{Name: t.clusterHealth.Name}, current)
	if err != nil {
		return nil
	}

	for i := range current.Status.ClusterConditions {
		cc := &current.Status.ClusterConditions[i]
		if isClusterConditionForCluster(cc, kindWorkloadCluster.GetNamespace(), kindWorkloadCluster.GetName()) {
			return cc
		}
	}

	return nil
}

// isFailingWith returns true when the evaluation of the cluster reports a failure with the given detail
func (t *notificationPolicyTest) isFailingWith(detail string) bool {
	cc := t.clusterCondition()
	return cc != nil && len(cc.Conditions) == 1 &&
		cc.Conditions[0].Status != corev1.ConditionTrue && strings.Contains(cc.Conditions[0].Message, detail)
}

// isPassing returns true when the evaluation of the cluster reports all liveness checks passing
func (t *notificationPolicyTest) isPassing() bool {
	cc := t.clusterCondition()
	return cc != nil && len(cc.Conditions) == 1 && cc.Conditions[0].Status == corev1.ConditionTrue
}

// failingSince returns when the cluster started to fail, as recorded in the status
func (t *notificationPolicyTest) failingSince() time.Time {
	cc := t.clusterCondition()
	Expect(cc).ToNot(BeNil())
	Expect(cc.Conditions).To(HaveLen(1))
	return cc.Conditions[0].LastTransitionTime.Time
}

// summary returns what the status records about the delivery of the notification. Nil if nothing is recorded.
func (t *notificationPolicyTest) summary() *libsveltosv1beta1.NotificationSummary {
	cc := t.clusterCondition()
	if cc == nil || len(cc.NotificationSummaries) != 1 {
		return nil
	}
	return &cc.NotificationSummaries[0]
}

// lastSent returns the time of the last delivery and whether the cluster was failing then.
// The time is zero if nothing was ever delivered.
func (t *notificationPolicyTest) lastSent() (time.Time, bool) {
	summary := t.summary()
	if summary == nil || summary.LastSentTime == nil || summary.LastSentFailing == nil {
		return time.Time{}, false
	}
	return summary.LastSentTime.Time, *summary.LastSentFailing
}

// waitForRecordedDelivery waits until the status records a delivery made while the cluster was in the given state.
// An event is created before the status is updated: what was delivered can be read from the status only after this.
func (t *notificationPolicyTest) waitForRecordedDelivery(failing bool) {
	Eventually(func() bool {
		lastSent, lastFailing := t.lastSent()
		return !lastSent.IsZero() && lastFailing == failing
	}, timeout, pollingInterval).Should(BeTrue())
}

// notes returns the message of all the events generated for the ClusterHealthCheck
func (t *notificationPolicyTest) notes() []string {
	eventList := &eventsv1.EventList{}
	if err := k8sClient.List(context.TODO(), eventList); err != nil {
		return nil
	}

	notes := make([]string, 0)
	for i := range eventList.Items {
		if eventList.Items[i].Regarding.Name == t.clusterHealth.Name {
			notes = append(notes, eventList.Items[i].Note)
		}
	}

	return notes
}

// hasNotification returns true when a notification with the given text has been delivered
func (t *notificationPolicyTest) hasNotification(text string) bool {
	for _, note := range t.notes() {
		if strings.Contains(note, text) {
			return true
		}
	}
	return false
}

// This test verifies that a notification with policy onlyOnTransition:
// - is not delivered when the cluster is found passing
// - is delivered when the cluster starts failing
// - is not delivered when the failure message changes while the cluster keeps failing
// - is delivered when the cluster recovers
var _ = Describe("Notification policy: onlyOnTransition", func() {
	It("Verifies only transitions are notified", Label("FV", "PULLMODE"), func() {
		t := newNotificationPolicyTest("notification-policy-transition-",
			&libsveltosv1beta1.NotificationPolicy{OnlyOnTransition: true})

		By("Verifying nothing is delivered for a cluster found passing")
		Consistently(func() bool {
			lastSent, _ := t.lastSent()
			return len(t.notes()) == 0 && lastSent.IsZero()
		}, quietPeriod, pollingInterval).Should(BeTrue())

		first := "first-" + randomString()
		t.setHealth(stateBroken, first)

		By("Verifying the failure is delivered")
		Eventually(func() bool { return t.hasNotification(first) }, timeout, pollingInterval).Should(BeTrue())
		t.waitForRecordedDelivery(true)
		deliveredAt, _ := t.lastSent()
		hash := t.summary().LastSentMessageHash

		second := "second-" + randomString()
		t.setHealth(stateBroken, second)

		By("Verifying the change of the failure message is evaluated but not delivered")
		Eventually(func() bool { return t.isFailingWith(second) }, timeout, pollingInterval).Should(BeTrue())
		Consistently(func() bool {
			lastSent, _ := t.lastSent()
			return !t.hasNotification(second) && lastSent.Equal(deliveredAt) && t.summary().LastSentMessageHash == hash
		}, quietPeriod, pollingInterval).Should(BeTrue())

		t.setHealth(stateHealthy, "")

		By("Verifying the recovery is delivered")
		Eventually(func() bool { return t.hasNotification(healthyMessage) }, timeout, pollingInterval).Should(BeTrue())
		t.waitForRecordedDelivery(false)
	})
})

// This test verifies that a notification with policy minInterval:
// - is delivered when the cluster starts failing
// - holds back the changes happening inside the interval
// - delivers, when the interval is over, only the state the cluster is in then
var _ = Describe("Notification policy: minInterval", func() {
	It("Verifies changes inside the interval are coalesced", Label("FV", "PULLMODE"), func() {
		const interval = 90 * time.Second

		t := newNotificationPolicyTest("notification-policy-interval-",
			&libsveltosv1beta1.NotificationPolicy{MinInterval: &metav1.Duration{Duration: interval}})

		first := "first-" + randomString()
		t.setHealth(stateBroken, first)

		By("Verifying the failure is delivered")
		Eventually(func() bool { return t.hasNotification(first) }, timeout, pollingInterval).Should(BeTrue())
		t.waitForRecordedDelivery(true)
		deliveredAt, _ := t.lastSent()

		second := "second-" + randomString()
		third := "third-" + randomString()

		t.setHealth(stateBroken, second)
		Eventually(func() bool { return t.isFailingWith(second) }, timeout, pollingInterval).Should(BeTrue())
		t.setHealth(stateBroken, third)
		Eventually(func() bool { return t.isFailingWith(third) }, timeout, pollingInterval).Should(BeTrue())

		By("Verifying the changes are evaluated but not delivered inside the interval")
		Consistently(func() bool {
			lastSent, _ := t.lastSent()
			return time.Since(deliveredAt) >= interval || // the interval is over: nothing to check
				(!t.hasNotification(second) && !t.hasNotification(third) && lastSent.Equal(deliveredAt))
		}, quietPeriod, pollingInterval).Should(BeTrue())

		By("Verifying, once the interval is over, the current state is delivered")
		Eventually(func() bool { return t.hasNotification(third) }, timeout, pollingInterval).Should(BeTrue())
		lastSent, _ := t.lastSent()
		// Times in the status have a precision of one second
		Expect(lastSent.Sub(deliveredAt)).To(BeNumerically(">=", interval-2*time.Second))

		By("Verifying the intermediate state was never delivered")
		Expect(t.hasNotification(second)).To(BeFalse())
	})
})

// This test verifies that a notification with policy failingFor:
// - holds back a failure until it lasted long enough
// - delivers the failure and then its recovery
// - never delivers a failure that ended before lasting long enough, nor its recovery
var _ = Describe("Notification policy: failingFor", func() {
	It("Verifies a failure is delivered only after it lasted long enough", Label("FV", "PULLMODE"), func() {
		const failingFor = 90 * time.Second

		t := newNotificationPolicyTest("notification-policy-failing-for-",
			&libsveltosv1beta1.NotificationPolicy{FailingFor: &metav1.Duration{Duration: failingFor}})

		first := "first-" + randomString()
		t.setHealth(stateBroken, first)
		Eventually(func() bool { return t.isFailingWith(first) }, timeout, pollingInterval).Should(BeTrue())
		failureStarted := t.failingSince()

		By("Verifying the failure is held back while it has not lasted long enough")
		Consistently(func() bool {
			return time.Since(failureStarted) >= failingFor || !t.hasNotification(first)
		}, quietPeriod, pollingInterval).Should(BeTrue())

		By("Verifying the failure is delivered once it lasted long enough")
		Eventually(func() bool { return t.hasNotification(first) }, timeout, pollingInterval).Should(BeTrue())
		t.waitForRecordedDelivery(true)
		deliveredAt, _ := t.lastSent()
		// Times in the status have a precision of one second
		Expect(deliveredAt.Sub(failureStarted)).To(BeNumerically(">=", failingFor-2*time.Second))

		t.setHealth(stateHealthy, "")

		By("Verifying the recovery of a failure that was delivered is delivered")
		Eventually(func() bool { return t.hasNotification(healthyMessage) }, timeout, pollingInterval).Should(BeTrue())
		t.waitForRecordedDelivery(false)
		recoveredAt, _ := t.lastSent()

		By("Verifying a failure ending before lasting long enough is not delivered")
		second := "second-" + randomString()
		t.setHealth(stateBroken, second)
		Eventually(func() bool { return t.isFailingWith(second) }, timeout, pollingInterval).Should(BeTrue())
		t.setHealth(stateHealthy, "")
		Eventually(func() bool { return t.isPassing() }, timeout, pollingInterval).Should(BeTrue())

		By("Verifying neither the failure nor its recovery are delivered")
		Consistently(func() bool {
			lastSent, failing := t.lastSent()
			return !t.hasNotification(second) && lastSent.Equal(recoveredAt) && !failing
		}, quietPeriod, pollingInterval).Should(BeTrue())
	})
})
