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

package controllers_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"

	"github.com/projectsveltos/healthcheck-manager/controllers"
	libsveltosv1alpha1 "github.com/projectsveltos/libsveltos/api/v1alpha1"
)

var _ = Describe("Notification", func() {
	var n *libsveltosv1alpha1.Notification

	BeforeEach(func() {
		n = &libsveltosv1alpha1.Notification{
			Name: randomString(),
			Type: libsveltosv1alpha1.NotificationTypeKubernetesEvent,
		}

	})

	It("doSendNotification returns true when resendAll is true", func() {
		Expect(controllers.DoSendNotification(n, nil, true)).To(BeTrue())
	})

	It("doSendNotification returns true when notification has never been delivered before", func() {
		Expect(controllers.DoSendNotification(n, nil, false)).To(BeTrue())
	})

	It("doSendNotification returns false when nothing has changed and notification was alreayd delivered",
		func() {
			status := make(map[string]libsveltosv1alpha1.NotificationStatus)
			status[n.Name] = libsveltosv1alpha1.NotificationStatusDelivered
			Expect(controllers.DoSendNotification(n, status, false)).To(BeFalse())
		})

	It("buildNotificationStatusMap creates map with status for each notification", func() {
		clusterNamespace := randomString()
		clusterName := randomString()
		clusterType := libsveltosv1alpha1.ClusterTypeCapi

		notificationName := randomString()

		chc := &libsveltosv1alpha1.ClusterHealthCheck{
			ObjectMeta: metav1.ObjectMeta{
				Name: randomString(),
			},
			Status: libsveltosv1alpha1.ClusterHealthCheckStatus{
				ClusterConditions: []libsveltosv1alpha1.ClusterCondition{
					{
						ClusterInfo: libsveltosv1alpha1.ClusterInfo{
							Cluster: corev1.ObjectReference{
								Namespace:  clusterNamespace,
								Name:       clusterName,
								Kind:       "Cluster",
								APIVersion: clusterv1.GroupVersion.String(),
							},
						},
						NotificationSummaries: []libsveltosv1alpha1.NotificationSummary{
							{
								Name:   notificationName,
								Status: libsveltosv1alpha1.NotificationStatusDelivered,
							},
						},
					},
				},
			},
		}

		result := controllers.BuildNotificationStatusMap(clusterNamespace, clusterName, clusterType, chc)
		Expect(result).ToNot(BeNil())
		status, ok := result[notificationName]
		Expect(ok).To(BeTrue())
		Expect(status).To(Equal(libsveltosv1alpha1.NotificationStatusDelivered))
	})
})
