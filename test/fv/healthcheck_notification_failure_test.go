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
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
)

// This test verifies that a notification failing to deliver (here: a Slack notification
// whose NotificationRef points at a Secret that does not exist) does not get treated as a
// deployment failure. The ClusterHealthCheck must stay Provisioned, liveness Conditions must
// still be recorded, and the failure must be visible per-channel in NotificationSummaries.
var _ = Describe("Liveness: healthCheck Notifications: failed delivery", func() {
	const (
		namePrefix = "healthcheck-notification-failure-"
	)

	It("Verifies a failed notification does not affect ClusterHealthCheck deployment status",
		Label("FV", "PULLMODE"), func() {
			healthCheck := &libsveltosv1beta1.HealthCheck{
				ObjectMeta: metav1.ObjectMeta{
					Name: namePrefix + randomString(),
				},
				Spec: libsveltosv1beta1.HealthCheckSpec{
					ResourceSelectors: []libsveltosv1beta1.ResourceSelector{
						{
							Group:   groupApps,
							Version: apiVersionV1,
							Kind:    deploymentKind,
							LabelFilters: []libsveltosv1beta1.LabelFilter{
								{Key: controlPlaneLabelKey, Operation: libsveltosv1beta1.OperationEqual, Value: sveltosAgentLabelValue},
							},
						},
					},
					EvaluateHealth: evaluateFunction,
				},
			}

			By(fmt.Sprintf("Creating healthCheck %s", healthCheck.Name))
			Expect(k8sClient.Create(context.TODO(), healthCheck)).To(Succeed())

			lc := libsveltosv1beta1.LivenessCheck{
				Name: randomString(),
				Type: libsveltosv1beta1.LivenessTypeHealthCheck,
				LivenessSourceRef: &corev1.ObjectReference{
					Name:       healthCheck.Name,
					APIVersion: libsveltosv1beta1.GroupVersion.String(),
					Kind:       libsveltosv1beta1.HealthCheckKind,
				},
			}

			// Deliberately do not create this Secret: the Slack notification must fail to
			// deliver because of it.
			notification := libsveltosv1beta1.Notification{
				Name: randomString(),
				Type: libsveltosv1beta1.NotificationTypeSlack,
				NotificationRef: &corev1.ObjectReference{
					Kind:       "Secret",
					APIVersion: apiVersionV1,
					Namespace:  "default",
					Name:       namePrefix + randomString(),
				},
			}

			Byf("Create a ClusterHealthCheck matching Cluster %s/%s", kindWorkloadCluster.GetNamespace(), kindWorkloadCluster.GetName())
			clusterHealthCheck := getClusterHealthCheck(namePrefix, map[string]string{key: value},
				[]libsveltosv1beta1.LivenessCheck{lc}, []libsveltosv1beta1.Notification{notification})
			Expect(k8sClient.Create(context.TODO(), clusterHealthCheck)).To(Succeed())

			Byf("Verifying ClusterHealthCheck %s stays Provisioned with the liveness check recorded "+
				"and the notification reported as failed to deliver", clusterHealthCheck.Name)
			Eventually(func() bool {
				currentClusterHealthCheck := &libsveltosv1beta1.ClusterHealthCheck{}
				err := k8sClient.Get(context.TODO(),
					types.NamespacedName{Name: clusterHealthCheck.Name}, currentClusterHealthCheck)
				if err != nil {
					return false
				}

				for i := range currentClusterHealthCheck.Status.ClusterConditions {
					cc := &currentClusterHealthCheck.Status.ClusterConditions[i]
					if !isClusterConditionForCluster(cc, kindWorkloadCluster.GetNamespace(), kindWorkloadCluster.GetName()) {
						continue
					}

					if cc.ClusterInfo.Status != libsveltosv1beta1.SveltosStatusProvisioned {
						return false
					}
					if cc.ClusterInfo.FailureMessage != nil {
						return false
					}
					if len(cc.Conditions) == 0 || cc.Conditions[0].Status != corev1.ConditionTrue {
						return false
					}
					if len(cc.NotificationSummaries) != 1 {
						return false
					}
					ns := &cc.NotificationSummaries[0]
					return ns.Status == libsveltosv1beta1.NotificationStatusFailedToDeliver && ns.FailureMessage != nil
				}
				return false
			}, timeout, pollingInterval).Should(BeTrue())

			Byf("Deleting ClusterHealthCheck")
			deleteClusterHealthCheck(clusterHealthCheck.Name)

			Byf("Deleting HealthCheck")
			Expect(k8sClient.Delete(context.TODO(), healthCheck)).To(Succeed())
		})
})
