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
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
)

// This test verifies that a ClusterHealthCheck with:
// - two liveness checks of type HealthCheck
// cleans up everything it created when the cluster stops matching its clusterSelector:
//   - HealthChecks are removed from the managed cluster;
//   - nothing is left in the management cluster (ConfigurationGroups and ConfigurationBundles used in pull mode,
//     HealthCheckReports);
//   - the cluster is not tracked in the ClusterHealthCheck status anymore.
var _ = Describe("Liveness: cluster stops matching", func() {
	const (
		namePrefix = "cluster-stops-matching-"
	)

	It("Verifies everything is removed when the cluster stops matching the ClusterHealthCheck",
		Label("FV", "PULLMODE"), func() {

			healthChecks := []*libsveltosv1beta1.HealthCheck{newDeploymentHealthCheck(), newDeploymentHealthCheck()}
			livenessChecks := make([]libsveltosv1beta1.LivenessCheck, 0, len(healthChecks))
			for _, healthCheck := range healthChecks {
				By(fmt.Sprintf("Creating healthCheck %s", healthCheck.Name))
				Expect(k8sClient.Create(context.TODO(), healthCheck)).To(Succeed())
				livenessChecks = append(livenessChecks, newHealthCheckLivenessCheck(healthCheck))
			}

			Byf("Create a ClusterHealthCheck matching Cluster %s/%s", kindWorkloadCluster.GetNamespace(), kindWorkloadCluster.GetName())
			clusterHealthCheck := getClusterHealthCheck(namePrefix, map[string]string{key: value},
				livenessChecks, []libsveltosv1beta1.Notification{})
			Expect(k8sClient.Create(context.TODO(), clusterHealthCheck)).To(Succeed())

			workloadClient, err := getKindWorkloadClusterKubeconfig()
			Expect(err).To(BeNil())
			Expect(workloadClient).ToNot(BeNil())

			for _, healthCheck := range healthChecks {
				if !isAgentLessMode() {
					Byf("Verifying HealthCheck %s is deployed in the manged cluster", healthCheck.Name)
					Eventually(func() error {
						return workloadClient.Get(context.TODO(), types.NamespacedName{Name: healthCheck.Name},
							&libsveltosv1beta1.HealthCheck{})
					}, timeout, pollingInterval).Should(BeNil())
				}
				verifyHealthCheckReportInManagementCluster(healthCheck.Name)
			}

			Byf("Changing the ClusterHealthCheck clusterSelector so Cluster %s/%s does not match anymore",
				kindWorkloadCluster.GetNamespace(), kindWorkloadCluster.GetName())
			setNotMatchingClusterSelector(clusterHealthCheck.Name)

			Byf("Verifying the cluster is not tracked in the ClusterHealthCheck status anymore")
			Eventually(func() bool {
				currentClusterHealthCheck := &libsveltosv1beta1.ClusterHealthCheck{}
				err := k8sClient.Get(context.TODO(), types.NamespacedName{Name: clusterHealthCheck.Name},
					currentClusterHealthCheck)
				if err != nil {
					return false
				}
				for i := range currentClusterHealthCheck.Status.ClusterConditions {
					cc := &currentClusterHealthCheck.Status.ClusterConditions[i]
					if isClusterConditionForCluster(cc, kindWorkloadCluster.GetNamespace(), kindWorkloadCluster.GetName()) {
						return false
					}
				}
				return true
			}, timeout, pollingInterval).Should(BeTrue())

			for _, healthCheck := range healthChecks {
				if !isAgentLessMode() {
					Byf("Verifying HealthCheck %s is removed from the manged cluster", healthCheck.Name)
					Eventually(func() bool {
						err := workloadClient.Get(context.TODO(), types.NamespacedName{Name: healthCheck.Name},
							&libsveltosv1beta1.HealthCheck{})
						return err != nil && (apierrors.IsNotFound(err) || meta.IsNoMatchError(err))
					}, timeout, pollingInterval).Should(BeTrue())
				}
			}

			verifyNoConfigurationResourcesInManagementCluster(clusterHealthCheck.Name)
			verifyHealthCheckReportsRemovedFromManagementCluster(getReferencedHealthCheckNames(clusterHealthCheck)...)

			deleteClusterHealthCheck(clusterHealthCheck.Name)

			for _, healthCheck := range healthChecks {
				Expect(client.IgnoreNotFound(k8sClient.Delete(context.TODO(), healthCheck))).To(Succeed())
			}
		})
})

// newDeploymentHealthCheck returns a HealthCheck evaluating the Deployments of sveltos-agent
func newDeploymentHealthCheck() *libsveltosv1beta1.HealthCheck {
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

// newHealthCheckLivenessCheck returns a liveness check referencing the HealthCheck. Its name is not the name of the
// HealthCheck, as it is for any real ClusterHealthCheck.
func newHealthCheckLivenessCheck(healthCheck *libsveltosv1beta1.HealthCheck) libsveltosv1beta1.LivenessCheck {
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

// setNotMatchingClusterSelector changes the clusterSelector of the ClusterHealthCheck so no cluster matches
func setNotMatchingClusterSelector(clusterHealthCheckName string) {
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		currentClusterHealthCheck := &libsveltosv1beta1.ClusterHealthCheck{}
		err := k8sClient.Get(context.TODO(), types.NamespacedName{Name: clusterHealthCheckName},
			currentClusterHealthCheck)
		if err != nil {
			return err
		}

		currentClusterHealthCheck.Spec.ClusterSelector = libsveltosv1beta1.Selector{
			LabelSelector: metav1.LabelSelector{
				MatchLabels: map[string]string{key: randomString()},
			},
		}
		return k8sClient.Update(context.TODO(), currentClusterHealthCheck)
	})
	Expect(err).To(BeNil())
}
