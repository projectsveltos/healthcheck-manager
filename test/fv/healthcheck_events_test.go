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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	libsveltosv1alpha1 "github.com/projectsveltos/libsveltos/api/v1alpha1"
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
		healthCheck := &libsveltosv1alpha1.HealthCheck{
			ObjectMeta: metav1.ObjectMeta{
				Name: randomString(),
			},
			Spec: libsveltosv1alpha1.HealthCheckSpec{
				ResourceSelectors: []libsveltosv1alpha1.ResourceSelector{
					{
						Group:   "apps",
						Version: "v1",
						Kind:    "Deployment",
						LabelFilters: []libsveltosv1alpha1.LabelFilter{
							{Key: "control-plane", Operation: libsveltosv1alpha1.OperationEqual, Value: "sveltos-agent"},
						},
					},
				},
				EvaluateHealth: `
   function evaluate()
     statuses = {}
     status = "Progressing"
     message = ""
	 for _, resource in ipairs(resources) do
       if resource.status ~= nil then
         if resource.status.availableReplicas ~= nil then
           if resource.status.availableReplicas == resource.spec.replicas then
             status = "Healthy"
           end
           if resource.status.availableReplicas ~= resource.spec.replicas then
             status = "Progressing"
             message = "expected replicas: " .. resource.spec.replicas .. " available: " .. resource.status.availableReplicas
           end
         end
       end
	   table.insert(statuses, {resource=resource, status=status, message=message}) 
	 end  
	 local hs = {}
	 if #statuses > 0 then
	   hs.resources = statuses 
	 end
	 return hs
   end`,
			},
		}

		By(fmt.Sprintf("Creating healthCheck %s", healthCheck.Name))
		Expect(k8sClient.Create(context.TODO(), healthCheck)).To(Succeed())

		lc := libsveltosv1alpha1.LivenessCheck{
			Name: randomString(),
			Type: libsveltosv1alpha1.LivenessTypeHealthCheck,
			LivenessSourceRef: &corev1.ObjectReference{
				Name:       healthCheck.Name,
				APIVersion: libsveltosv1alpha1.GroupVersion.String(),
				Kind:       libsveltosv1alpha1.HealthCheckKind,
			},
		}

		notification := libsveltosv1alpha1.Notification{Name: randomString(), Type: libsveltosv1alpha1.NotificationTypeKubernetesEvent}

		Byf("Create a ClusterHealthCheck matching Cluster %s/%s", kindWorkloadCluster.Namespace, kindWorkloadCluster.Name)
		clusterHealthCheck := getClusterHealthCheck(namePrefix, map[string]string{key: value},
			[]libsveltosv1alpha1.LivenessCheck{lc}, []libsveltosv1alpha1.Notification{notification})
		Expect(k8sClient.Create(context.TODO(), clusterHealthCheck)).To(Succeed())

		Byf("Getting client to access the workload cluster")
		workloadClient, err := getKindWorkloadClusterKubeconfig()
		Expect(err).To(BeNil())
		Expect(workloadClient).ToNot(BeNil())

		By("Verifying HealthCheck is deployed in the manged cluster")
		Eventually(func() error {
			currentHealthCheck := &libsveltosv1alpha1.HealthCheck{}
			return workloadClient.Get(context.TODO(), types.NamespacedName{Name: healthCheck.Name},
				currentHealthCheck)
		}, timeout, pollingInterval).Should(BeNil())

		By(fmt.Sprintf("Verifying healthCheckReport projectsveltos/%s exists", healthCheck.Name))
		Eventually(func() error {
			healthCheckReport := &libsveltosv1alpha1.HealthCheckReport{}
			return workloadClient.Get(context.TODO(),
				types.NamespacedName{Namespace: "projectsveltos", Name: healthCheck.Name},
				healthCheckReport)
		}, timeout, pollingInterval).Should(BeNil())

		By("Verifying healthCheckReport exists in the management cluster")
		Eventually(func() bool {
			clusterType := libsveltosv1alpha1.ClusterTypeCapi
			labels := libsveltosv1alpha1.GetHealthCheckReportLabels(healthCheck.Name,
				kindWorkloadCluster.Name, &clusterType)
			listOptions := []client.ListOption{
				client.InNamespace(kindWorkloadCluster.Namespace),
				client.MatchingLabels(labels),
			}
			healthCheckReportList := &libsveltosv1alpha1.HealthCheckReportList{}
			err = k8sClient.List(context.TODO(), healthCheckReportList, listOptions...)
			return err == nil && len(healthCheckReportList.Items) == 1
		}, timeout, pollingInterval).Should(BeTrue())

		Byf("Verifying ClusterHealthCheck %s is set to Provisioned", clusterHealthCheck.Name)
		verifyClusterHealthCheckStatus(clusterHealthCheck.Name, kindWorkloadCluster.Namespace, kindWorkloadCluster.Name)

		Byf("Deleting ClusterHealthCheck")
		deleteClusterHealthCheck(clusterHealthCheck.Name)

		Byf("Verifying healthCheckReport is removed (or mark as processed) from managed cluster")
		Eventually(func() bool {
			currentHealthCheckReport := &libsveltosv1alpha1.HealthCheckReport{}
			err = workloadClient.Get(context.TODO(),
				types.NamespacedName{Namespace: "projectsveltos", Name: healthCheck.Name},
				currentHealthCheckReport)
			if err != nil {
				return apierrors.IsNotFound(err)
			} else {
				return currentHealthCheckReport.Status.Phase != nil &&
					*currentHealthCheckReport.Status.Phase == libsveltosv1alpha1.ReportProcessed
			}
		}, timeout, pollingInterval).Should(BeTrue())

		By("Verifying HealthCheck is removed in the manged cluster")
		Eventually(func() bool {
			currentHealthCheck := &libsveltosv1alpha1.HealthCheck{}
			err = workloadClient.Get(context.TODO(), types.NamespacedName{Name: healthCheck.Name},
				currentHealthCheck)
			return err != nil && apierrors.IsNotFound(err)
		}, timeout, pollingInterval).Should(BeTrue())
	})
})
