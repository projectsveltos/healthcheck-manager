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

package fv_test

import (
	"context"
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
)

// This test verifies that a ClusterHealthCheck with:
// - liveness check of type add-ons
// - notifications of type Kubernetes events
// when add-ons are deployed, event is generated
var _ = Describe("Liveness: healthCheck Notifications: events", func() {
	const (
		namePrefix = "healthcheck-events-"
	)

	It("Verifies healthCheck events are delivered", Label("FV"), func() {
		healthCheck := &libsveltosv1beta1.HealthCheck{
			ObjectMeta: metav1.ObjectMeta{
				Name: randomString(),
			},
			Spec: libsveltosv1beta1.HealthCheckSpec{
				ResourceSelectors: []libsveltosv1beta1.ResourceSelector{
					{
						Group:   "apps",
						Version: "v1",
						Kind:    "Deployment",
						LabelFilters: []libsveltosv1beta1.LabelFilter{
							{Key: "control-plane", Operation: libsveltosv1beta1.OperationEqual, Value: "sveltos-agent"},
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

		notification := libsveltosv1beta1.Notification{Name: randomString(), Type: libsveltosv1beta1.NotificationTypeKubernetesEvent}

		Byf("Create a ClusterHealthCheck matching Cluster %s/%s", kindWorkloadCluster.Namespace, kindWorkloadCluster.Name)
		clusterHealthCheck := getClusterHealthCheck(namePrefix, map[string]string{key: value},
			[]libsveltosv1beta1.LivenessCheck{lc}, []libsveltosv1beta1.Notification{notification})
		Expect(k8sClient.Create(context.TODO(), clusterHealthCheck)).To(Succeed())

		Byf("Getting client to access the workload cluster")
		workloadClient, err := getKindWorkloadClusterKubeconfig()
		Expect(err).To(BeNil())
		Expect(workloadClient).ToNot(BeNil())

		if isAgentLessMode() {
			By("Verifying HealthCheck is NOT present in the manged cluster")
			Eventually(func() bool {
				currentHealthCheck := &libsveltosv1beta1.HealthCheck{}
				err = workloadClient.Get(context.TODO(), types.NamespacedName{Name: healthCheck.Name},
					currentHealthCheck)
				return err != nil && meta.IsNoMatchError(err) // CRD never installed
			}, timeout, pollingInterval).Should(BeTrue())

			By("Verifying healthCheckReport NOT present in the managed cluster")
			Eventually(func() bool {
				healthCheckReport := &libsveltosv1beta1.HealthCheckReport{}
				err = workloadClient.Get(context.TODO(),
					types.NamespacedName{Namespace: "projectsveltos", Name: healthCheck.Name},
					healthCheckReport)
				return err != nil && meta.IsNoMatchError(err) // CRD never installed
			}, timeout, pollingInterval).Should(BeTrue())
		} else {
			By("Verifying HealthCheck is deployed in the manged cluster")
			Eventually(func() error {
				currentHealthCheck := &libsveltosv1beta1.HealthCheck{}
				return workloadClient.Get(context.TODO(), types.NamespacedName{Name: healthCheck.Name},
					currentHealthCheck)
			}, timeout, pollingInterval).Should(BeNil())

			Byf("Verifying healthCheckReport projectsveltos/%s exists", healthCheck.Name)
			Eventually(func() error {
				healthCheckReport := &libsveltosv1beta1.HealthCheckReport{}
				return workloadClient.Get(context.TODO(),
					types.NamespacedName{Namespace: "projectsveltos", Name: healthCheck.Name},
					healthCheckReport)
			}, timeout, pollingInterval).Should(BeNil())
		}

		By("Verifying healthCheckReport exists in the management cluster")
		Eventually(func() bool {
			clusterType := libsveltosv1beta1.ClusterTypeCapi
			labels := libsveltosv1beta1.GetHealthCheckReportLabels(healthCheck.Name,
				kindWorkloadCluster.Name, &clusterType)
			listOptions := []client.ListOption{
				client.InNamespace(kindWorkloadCluster.Namespace),
				client.MatchingLabels(labels),
			}
			healthCheckReportList := &libsveltosv1beta1.HealthCheckReportList{}
			err = k8sClient.List(context.TODO(), healthCheckReportList, listOptions...)
			return err == nil && len(healthCheckReportList.Items) == 1
		}, timeout, pollingInterval).Should(BeTrue())

		Byf("Verifying ClusterHealthCheck %s is set to Provisioned", clusterHealthCheck.Name)
		verifyClusterHealthCheckStatus(clusterHealthCheck.Name, kindWorkloadCluster.Namespace, kindWorkloadCluster.Name)

		Byf("Deleting ClusterHealthCheck")
		deleteClusterHealthCheck(clusterHealthCheck.Name)

		if !isAgentLessMode() {
			Byf("Verifying healthCheckReport is removed (or mark as processed) from managed cluster")
			Eventually(func() bool {
				currentHealthCheckReport := &libsveltosv1beta1.HealthCheckReport{}
				err = workloadClient.Get(context.TODO(),
					types.NamespacedName{Namespace: "projectsveltos", Name: healthCheck.Name},
					currentHealthCheckReport)
				if err != nil {
					return apierrors.IsNotFound(err)
				} else {
					return currentHealthCheckReport.Status.Phase != nil &&
						*currentHealthCheckReport.Status.Phase == libsveltosv1beta1.ReportProcessed
				}
			}, timeout, pollingInterval).Should(BeTrue())

			By("Verifying HealthCheck is removed in the manged cluster")
			Eventually(func() bool {
				currentHealthCheck := &libsveltosv1beta1.HealthCheck{}
				err = workloadClient.Get(context.TODO(), types.NamespacedName{Name: healthCheck.Name},
					currentHealthCheck)
				return err != nil && apierrors.IsNotFound(err)
			}, timeout, pollingInterval).Should(BeTrue())
		}
	})
})
