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

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
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

	It("Fetch a ReloaderReport and update listed resources", Label("FV", "PULLMODE"), func() {
		Byf("Getting client to access the workload cluster")
		workloadClient, err := getKindWorkloadClusterKubeconfig()
		Expect(err).To(BeNil())
		Expect(workloadClient).ToNot(BeNil())

		nsName := namePrefix + randomString()

		deployment, err := k8s_utils.GetUnstructured([]byte(fmt.Sprintf(deploymentToReload, nsName)))
		Expect(err).To(BeNil())

		Byf("Creating namespace for ConfigMap in management cluster")
		configMapNs := randomString()
		configMapNamespace := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: configMapNs,
			},
		}
		Expect(k8sClient.Create(context.TODO(), configMapNamespace)).To(Succeed())
		DeferCleanup(func() {
			Expect(k8sClient.Delete(context.TODO(), configMapNamespace)).To(Succeed())
		})

		Byf("Creating ConfigMap with namespace and deployment resources for managed cluster")
		configMap := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: configMapNs,
				Name:      namePrefix + randomString(),
			},
			Data: map[string]string{
				"namespace.yaml": fmt.Sprintf(`apiVersion: v1
kind: Namespace
metadata:
  name: %s`, nsName),
				"deployment.yaml": fmt.Sprintf(deploymentToReload, nsName),
			},
		}
		Expect(k8sClient.Create(context.TODO(), configMap)).To(Succeed())

		Byf("Creating ClusterProfile with Reloader=true and PolicyRefs referencing the ConfigMap")
		clusterProfile := &configv1beta1.ClusterProfile{
			ObjectMeta: metav1.ObjectMeta{
				Name: namePrefix + randomString(),
			},
			Spec: configv1beta1.Spec{
				ClusterSelector: libsveltosv1beta1.Selector{
					LabelSelector: metav1.LabelSelector{
						MatchLabels: map[string]string{key: value},
					},
				},
				Reloader: true,
				PolicyRefs: []configv1beta1.PolicyRef{
					{
						Kind:      string(libsveltosv1beta1.ConfigMapReferencedResourceKind),
						Namespace: configMap.Namespace,
						Name:      configMap.Name,
					},
				},
			},
		}
		Expect(k8sClient.Create(context.TODO(), clusterProfile)).To(Succeed())

		verifyClusterProfileMatches(clusterProfile)
		clusterSummary := verifyClusterSummary(clusterProfile,
			kindWorkloadCluster.GetNamespace(), kindWorkloadCluster.GetName())

		Byf("Verifying ClusterSummary %s resources are provisioned", clusterSummary.Name)
		verifyFeatureStatusIsProvisioned(clusterSummary.Namespace, clusterSummary.Name,
			libsveltosv1beta1.FeatureResources)

		Byf("Waiting for deployment %s/%s to be deployed in the managed cluster",
			deployment.GetNamespace(), deployment.GetName())
		Eventually(func() bool {
			currentDepl := &appsv1.Deployment{}
			return workloadClient.Get(context.TODO(),
				types.NamespacedName{Namespace: deployment.GetNamespace(), Name: deployment.GetName()},
				currentDepl) == nil
		}, timeout, pollingInterval).Should(BeTrue())

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
		if isAgentLessMode() {
			// Those are only set in agentless mode. With agents in the managed clusters,
			// Sveltos ignores reloaderReports collected with those fields set
			clusterType := libsveltosv1beta1.ClusterTypeCapi
			if kindWorkloadCluster.GetKind() == libsveltosv1beta1.SveltosClusterKind {
				clusterType = libsveltosv1beta1.ClusterTypeSveltos
			}
			reloaderReport.Spec.ClusterNamespace = kindWorkloadCluster.GetNamespace()
			reloaderReport.Spec.ClusterName = kindWorkloadCluster.GetName()
			reloaderReport.Spec.ClusterType = clusterType
			Expect(k8sClient.Create(context.TODO(), reloaderReport)).To(BeNil())
		} else {
			Expect(workloadClient.Create(context.TODO(), reloaderReport)).To(BeNil())
		}

		if isAgentLessMode() {
			Byf("Verifying ReloaderReport %s/%s is removed from management cluster",
				reloaderReport.Namespace, reloaderReport.Name)
			Eventually(func() bool {
				currentReloaderReport := &libsveltosv1beta1.ReloaderReport{}
				err = k8sClient.Get(context.TODO(),
					types.NamespacedName{Namespace: reloaderReport.Namespace, Name: reloaderReport.Name},
					currentReloaderReport)
				if err != nil {
					return apierrors.IsNotFound(err)
				}
				return false
			}, timeout, pollingInterval).Should(BeTrue())
		} else {
			Byf("Verifying ReloaderReport %s/%s is removed from managed cluster",
				reloaderReport.Namespace, reloaderReport.Name)
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
		}

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

		deleteClusterProfile(clusterProfile)
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
