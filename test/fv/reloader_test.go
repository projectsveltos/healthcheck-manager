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

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
	"github.com/projectsveltos/libsveltos/lib/k8s_utils"
)

var (
	deploymentToReload = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: nginx-deployment
  namespace: %s
spec:
  replicas: 3
  selector:
    matchLabels:
      app: nginx
  template:
    metadata:
      labels:
        app: nginx
    spec:
      containers:
        - name: nginx
          image: nginx:latest
          ports:
            - containerPort: 80`
)

// HealthCheck manager fetches ReloaderReports from managed cluster
// and triggers a rolling upgrade (by modifying container envs) for
// each resource (Deployment/StatefulSet/DaemonSet) listed in a ReloaderReport

// This test verifies that:
// - reloaderReports are fetched
// - resources (deployment in this test) listed in a ReloaderReport are updated
var _ = Describe("ReloaderReports processing", func() {
	const (
		namePrefix = "reloader-"
	)

	It("Fetch a ReloaderReport and update listed resources", Label("FV"), func() {
		Byf("Getting client to access the workload cluster")
		workloadClient, err := getKindWorkloadClusterKubeconfig()
		Expect(err).To(BeNil())
		Expect(workloadClient).ToNot(BeNil())

		nsName := namePrefix + randomString()
		Byf("Creating namespace in the managed cluster %s", nsName)
		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: nsName,
			},
		}
		Expect(workloadClient.Create(context.TODO(), ns)).To(Succeed())

		Byf("Create a deployment in namespace %s", nsName)
		deployment, err := k8s_utils.GetUnstructured([]byte(fmt.Sprintf(deploymentToReload, ns.Name)))
		Expect(err).To(BeNil())
		Expect(workloadClient.Create(context.TODO(), deployment)).To(BeNil())

		Byf("Create a reloaderReport listing deployoment %s/%s as resource to reload",
			deployment.GetNamespace(), deployment.GetName())
		reloaderReport := &libsveltosv1beta1.ReloaderReport{
			ObjectMeta: metav1.ObjectMeta{
				Name:      randomString(),
				Namespace: "projectsveltos",
				Annotations: map[string]string{
					libsveltosv1beta1.ReloaderReportResourceKindAnnotation:      "configMap",
					libsveltosv1beta1.ReloaderReportResourceNamespaceAnnotation: randomString(),
					libsveltosv1beta1.ReloaderReportResourceNameAnnotation:      randomString(),
				},
			},
			Spec: libsveltosv1beta1.ReloaderReportSpec{
				ResourcesToReload: []libsveltosv1beta1.ReloaderInfo{
					{
						Kind:      "Deployment",
						Namespace: deployment.GetNamespace(),
						Name:      deployment.GetName(),
					},
				},
			},
		}
		Expect(workloadClient.Create(context.TODO(), reloaderReport)).To(BeNil())

		Byf("Verifying ReloaderReport is removed from managed cluster")
		Eventually(func() bool {
			currentReloaderReport := &libsveltosv1beta1.ReloaderReport{}
			err = workloadClient.Get(context.TODO(),
				types.NamespacedName{Namespace: reloaderReport.Namespace, Name: reloaderReport.Name},
				currentReloaderReport)
			if err != nil {
				return apierrors.IsNotFound(err)
			}
			return false
		}, timeout, pollingInterval).Should(BeTrue())

		Byf("Verifying Deployment is marked for rolling upgrade")
		Eventually(func() bool {
			currentDeployment := &appsv1.Deployment{}
			err = workloadClient.Get(context.TODO(),
				types.NamespacedName{Namespace: deployment.GetNamespace(), Name: deployment.GetName()},
				currentDeployment)
			if err != nil {
				return false
			}
			for i := range currentDeployment.Spec.Template.Spec.Containers {
				c := &currentDeployment.Spec.Template.Spec.Containers[i]
				if !verifyEnvInContainer(c) {
					return false
				}
			}

			return true
		}, timeout, pollingInterval).Should(BeTrue())

		Byf("Deleting namespace %s", nsName)
		Expect(workloadClient.Delete(context.TODO(), ns)).To(Succeed())
	})
})

// verifyEnvInContainer verifies that env verifyEnvInContainer has
// been set on container
func verifyEnvInContainer(c *corev1.Container) bool {
	for i := range c.Env {
		env := c.Env[i]
		if env.Name == "PROJECTSVELTOS" {
			return true
		}
	}

	return false
}
