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
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/projectsveltos/healthcheck-manager/controllers"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
)

var _ = Describe("Notification", func() {
	var n *libsveltosv1beta1.Notification

	BeforeEach(func() {
		n = &libsveltosv1beta1.Notification{
			Name: randomString(),
			Type: libsveltosv1beta1.NotificationTypeKubernetesEvent,
		}

	})

	It("doSendNotification returns true when resendAll is true", func() {
		Expect(controllers.DoSendNotification(n, nil, true)).To(BeTrue())
	})

	It("doSendNotification returns true when notification has never been delivered before", func() {
		Expect(controllers.DoSendNotification(n, nil, false)).To(BeTrue())
	})

	It("doSendNotification returns false when nothing has changed and notification was already delivered",
		func() {
			status := make(map[string]libsveltosv1beta1.NotificationStatus)
			status[n.Name] = libsveltosv1beta1.NotificationStatusDelivered
			Expect(controllers.DoSendNotification(n, status, false)).To(BeFalse())
		})

	It("buildNotificationStatusMap creates map with status for each notification", func() {
		clusterNamespace := randomString()
		clusterName := randomString()
		clusterType := libsveltosv1beta1.ClusterTypeCapi

		notificationName := randomString()

		chc := &libsveltosv1beta1.ClusterHealthCheck{
			ObjectMeta: metav1.ObjectMeta{
				Name: randomString(),
			},
			Status: libsveltosv1beta1.ClusterHealthCheckStatus{
				ClusterConditions: []libsveltosv1beta1.ClusterCondition{
					{
						ClusterInfo: libsveltosv1beta1.ClusterInfo{
							Cluster: corev1.ObjectReference{
								Namespace:  clusterNamespace,
								Name:       clusterName,
								Kind:       "Cluster",
								APIVersion: clusterv1.GroupVersion.String(),
							},
						},
						NotificationSummaries: []libsveltosv1beta1.NotificationSummary{
							{
								Name:   notificationName,
								Status: libsveltosv1beta1.NotificationStatusDelivered,
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
		Expect(status).To(Equal(libsveltosv1beta1.NotificationStatusDelivered))
	})

	It("getWebexInfo get webex information from Secret", func() {
		webexRoomID := randomString()
		webexToken := randomString()
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      randomString(),
				Namespace: randomString(),
			},
			Type: libsveltosv1beta1.ClusterProfileSecretType,
			Data: map[string][]byte{
				libsveltosv1beta1.WebexRoomID: []byte(webexRoomID),
				libsveltosv1beta1.WebexToken:  []byte(webexToken),
			},
		}

		notification := &libsveltosv1beta1.Notification{
			Name: randomString(),
			Type: libsveltosv1beta1.NotificationTypeWebex,
			NotificationRef: &corev1.ObjectReference{
				Kind:       "Secret",
				APIVersion: "v1",
				Namespace:  secret.Namespace,
				Name:       secret.Name,
			},
		}

		initObjects := []client.Object{
			secret,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(initObjects...).
			WithObjects(initObjects...).Build()

		webexInfo, err := controllers.GetWebexInfo(context.TODO(), c, notification)
		Expect(err).To(BeNil())
		Expect(webexInfo).ToNot(BeNil())
		Expect(controllers.GetWebexRoom(webexInfo)).To(Equal(webexRoomID))
		Expect(controllers.GetWebexToken(webexInfo)).To(Equal(webexToken))
	})

	It("getSlackInfo get slack information from Secret", func() {
		slackChannelID := randomString()
		slackToken := randomString()
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      randomString(),
				Namespace: randomString(),
			},
			Type: libsveltosv1beta1.ClusterProfileSecretType,
			Data: map[string][]byte{
				libsveltosv1beta1.SlackChannelID: []byte(slackChannelID),
				libsveltosv1beta1.SlackToken:     []byte(slackToken),
			},
		}

		notification := &libsveltosv1beta1.Notification{
			Name: randomString(),
			Type: libsveltosv1beta1.NotificationTypeWebex,
			NotificationRef: &corev1.ObjectReference{
				Kind:       "Secret",
				APIVersion: "v1",
				Namespace:  secret.Namespace,
				Name:       secret.Name,
			},
		}

		initObjects := []client.Object{
			secret,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(initObjects...).
			WithObjects(initObjects...).Build()

		slackInfo, err := controllers.GetSlackInfo(context.TODO(), c, notification)
		Expect(err).To(BeNil())
		Expect(slackInfo).ToNot(BeNil())
		Expect(controllers.GetSlackChannelID(slackInfo)).To(Equal(slackChannelID))
		Expect(controllers.GetSlackToken(slackInfo)).To(Equal(slackToken))
	})
})
