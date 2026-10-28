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
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
)

// This test verifies that a ClusterHealthCheck with:
// - two liveness checks of type HealthCheck, each referencing a different HealthCheck
// deploys both HealthChecks to the managed cluster (in pull mode every HealthCheck must be staged in its own
// ConfigurationBundle) and that a HealthCheckReport is collected in the management cluster for each of them.
var _ = Describe("Liveness: multiple healthChecks", func() {
	const (
		namePrefix = "multiple-healthchecks-"
	)

	newHealthCheck := func() *libsveltosv1beta1.HealthCheck {
		return &libsveltosv1beta1.HealthCheck{
			ObjectMeta: metav1.ObjectMeta{
				Name: randomString(),
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
	}

	newLivenessCheck := func(healthCheck *libsveltosv1beta1.HealthCheck) libsveltosv1beta1.LivenessCheck {
		return libsveltosv1beta1.LivenessCheck{
			Name: randomString(),
			Type: libsveltosv1beta1.LivenessTypeHealthCheck,
			LivenessSourceRef: &corev1.ObjectReference{
				Name:       healthCheck.Name,
				APIVersion: libsveltosv1beta1.GroupVersion.String(),
				Kind:       libsveltosv1beta1.HealthCheckKind,
			},
		}
	}

	It("Verifies every healthCheck referenced by a ClusterHealthCheck is deployed", Label("FV", "PULLMODE"), func() {
		healthChecks := []*libsveltosv1beta1.HealthCheck{newHealthCheck(), newHealthCheck()}
		livenessChecks := make([]libsveltosv1beta1.LivenessCheck, 0, len(healthChecks))
		for _, healthCheck := range healthChecks {
			By(fmt.Sprintf("Creating healthCheck %s", healthCheck.Name))
			Expect(k8sClient.Create(context.TODO(), healthCheck)).To(Succeed())
			livenessChecks = append(livenessChecks, newLivenessCheck(healthCheck))
		}

		notification := libsveltosv1beta1.Notification{Name: randomString(), Type: libsveltosv1beta1.NotificationTypeKubernetesEvent}

		Byf("Create a ClusterHealthCheck matching Cluster %s/%s", kindWorkloadCluster.GetNamespace(), kindWorkloadCluster.GetName())
		clusterHealthCheck := getClusterHealthCheck(namePrefix, map[string]string{key: value},
			livenessChecks, []libsveltosv1beta1.Notification{notification})
		Expect(k8sClient.Create(context.TODO(), clusterHealthCheck)).To(Succeed())

		Byf("Getting client to access the workload cluster")
		workloadClient, err := getKindWorkloadClusterKubeconfig()
		Expect(err).To(BeNil())
		Expect(workloadClient).ToNot(BeNil())

		for i := range healthChecks {
			healthCheck := healthChecks[i]

			if isAgentLessMode() {
				By("Verifying HealthCheck is NOT present in the manged cluster")
				Eventually(func() bool {
					currentHealthCheck := &libsveltosv1beta1.HealthCheck{}
					err = workloadClient.Get(context.TODO(), types.NamespacedName{Name: healthCheck.Name},
						currentHealthCheck)
					return err != nil && meta.IsNoMatchError(err) // CRD never installed
				}, timeout, pollingInterval).Should(BeTrue())
			} else {
				Byf("Verifying HealthCheck %s is deployed in the manged cluster", healthCheck.Name)
				Eventually(func() error {
					currentHealthCheck := &libsveltosv1beta1.HealthCheck{}
					return workloadClient.Get(context.TODO(), types.NamespacedName{Name: healthCheck.Name},
						currentHealthCheck)
				}, timeout, pollingInterval).Should(BeNil())
			}

			verifyHealthCheckReportInManagementCluster(healthCheck.Name)
		}

		Byf("Verifying ClusterHealthCheck %s is set to Provisioned", clusterHealthCheck.Name)
		verifyClusterHealthCheckStatus(clusterHealthCheck.Name, kindWorkloadCluster.GetNamespace(), kindWorkloadCluster.GetName())

		// The agent deployed the HealthChecks: nothing needs to be kept in the management cluster
		verifyNoConfigurationResourcesInManagementCluster(clusterHealthCheck.Name)

		Byf("Deleting ClusterHealthCheck")
		deleteClusterHealthCheck(clusterHealthCheck.Name)

		if !isAgentLessMode() {
			for i := range healthChecks {
				healthCheck := healthChecks[i]
				Byf("Verifying HealthCheck %s is removed in the manged cluster", healthCheck.Name)
				Eventually(func() bool {
					currentHealthCheck := &libsveltosv1beta1.HealthCheck{}
					err = workloadClient.Get(context.TODO(), types.NamespacedName{Name: healthCheck.Name},
						currentHealthCheck)
					return err != nil && apierrors.IsNotFound(err)
				}, timeout, pollingInterval).Should(BeTrue())
			}
		}

		for _, healthCheck := range healthChecks {
			Expect(client.IgnoreNotFound(k8sClient.Delete(context.TODO(), healthCheck))).To(Succeed())
		}
	})
})
