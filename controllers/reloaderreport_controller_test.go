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

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2/klogr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/projectsveltos/healthcheck-manager/controllers"
	libsveltosv1alpha1 "github.com/projectsveltos/libsveltos/api/v1alpha1"
	"github.com/projectsveltos/libsveltos/lib/utils"
)

var (
	depl = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: example-deployment
  labels:
    app: example
spec:
  replicas: 3
  selector:
    matchLabels:
      app: example
  template:
    metadata:
      labels:
        app: example
    spec:
      containers:
        - name: example-app
          image: your-container-image:latest
          ports:
            - containerPort: 80`

	statefulSet = `apiVersion: apps/v1
kind: StatefulSet
metadata:
  name: example-statefulset
  namespace: default
  labels:
    app: example
spec:
  serviceName: example
  replicas: 3
  selector:
    matchLabels:
      app: example
  template:
    metadata:
      labels:
        app: example
    spec:
      containers:
        - name: example-app
          image: your-container-image:latest
          ports:
            - containerPort: 80
  volumeClaimTemplates:
    - metadata:
        name: data
      spec:
        accessModes: [ "ReadWriteOnce" ]
        resources:
          requests:
            storage: 10Gi`

	daemonSet = `apiVersion: apps/v1
kind: DaemonSet
metadata:
  name: example-daemonset
  namespace: default
  labels:
    app: example
spec:
  selector:
    matchLabels:
      app: example
  template:
    metadata:
      labels:
        app: example
    spec:
      containers:
        - name: example-app
          image: your-container-image:latest
          ports:
            - containerPort: 80`
)

var _ = Describe("ReloaderReport Controller", func() {
	It("updateEnvs updates envs", func() {
		reconciler := controllers.ReloaderReportReconciler{}

		value := randomString()

		var envs []corev1.EnvVar
		envs = controllers.UpdateEnvs(&reconciler, envs, value)
		Expect(len(envs)).To(Equal(1))
		Expect(envs[0].Name).To(Equal(controllers.SveltosEnv))
		Expect(envs[0].Value).To(Equal(value))

		value = randomString()
		envs = controllers.UpdateEnvs(&reconciler, envs, value)
		Expect(len(envs)).To(Equal(1))
		Expect(envs[0].Name).To(Equal(controllers.SveltosEnv))
		Expect(envs[0].Value).To(Equal(value))

		value = randomString()
		envs = append(envs, corev1.EnvVar{Name: randomString(), Value: randomString()})
		envs = controllers.UpdateEnvs(&reconciler, envs, value)
		Expect(len(envs)).To(Equal(2))
		Expect(envs).To(ContainElement(corev1.EnvVar{
			Name:  controllers.SveltosEnv,
			Value: value,
		}))
	})

	It("fetchAndPrepareDeployment fetches deployment and updates its containers' envs", func() {
		obj, err := utils.GetUnstructured([]byte(depl))
		Expect(err).To(BeNil())
		initObjects := []client.Object{
			obj,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(initObjects...).Build()

		reconciler := controllers.ReloaderReportReconciler{
			Client: c,
		}

		deplName := types.NamespacedName{Namespace: obj.GetNamespace(), Name: obj.GetName()}

		value := randomString()
		var currentObj client.Object
		currentObj, err = controllers.FetchAndPrepareDeployment(&reconciler, context.TODO(), c,
			&deplName, value, klogr.New())
		Expect(err).To(BeNil())

		currentDepl := currentObj.(*appsv1.Deployment)
		verifyEnvs(currentDepl.Spec.Template.Spec.Containers, value)
	})

	It("fetchAndPrepareStatefulSet fetches statefulSet and updates its containers' envs", func() {
		obj, err := utils.GetUnstructured([]byte(statefulSet))
		Expect(err).To(BeNil())
		initObjects := []client.Object{
			obj,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(initObjects...).Build()

		reconciler := controllers.ReloaderReportReconciler{
			Client: c,
		}

		statefulSetName := types.NamespacedName{Namespace: obj.GetNamespace(), Name: obj.GetName()}

		value := randomString()
		var currentObj client.Object
		currentObj, err = controllers.FetchAndPrepareStatefulSet(&reconciler, context.TODO(), c,
			&statefulSetName, value, klogr.New())
		Expect(err).To(BeNil())

		currentStatefulSet := currentObj.(*appsv1.StatefulSet)
		verifyEnvs(currentStatefulSet.Spec.Template.Spec.Containers, value)
	})

	It("fetchAndPrepareDaemonSet fetches daemonSet and updates its containers' envs", func() {
		obj, err := utils.GetUnstructured([]byte(daemonSet))
		Expect(err).To(BeNil())
		initObjects := []client.Object{
			obj,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(initObjects...).Build()

		reconciler := controllers.ReloaderReportReconciler{
			Client: c,
		}

		daemonSetName := types.NamespacedName{Namespace: obj.GetNamespace(), Name: obj.GetName()}

		value := randomString()
		var currentObj client.Object
		currentObj, err = controllers.FetchAndPrepareDaemonSet(&reconciler, context.TODO(), c,
			&daemonSetName, value, klogr.New())
		Expect(err).To(BeNil())

		currentDaemonSet := currentObj.(*appsv1.DaemonSet)
		verifyEnvs(currentDaemonSet.Spec.Template.Spec.Containers, value)
	})

	It("triggerRollingUpgrade returns no error when resource does not exist", func() {
		c := fake.NewClientBuilder().WithScheme(scheme).Build()

		reconciler := controllers.ReloaderReportReconciler{
			Client: c,
		}

		value := randomString()
		resourceToReload := &libsveltosv1alpha1.ReloaderInfo{
			Kind:      "Deployment",
			Namespace: randomString(),
			Name:      randomString(),
		}
		err := controllers.TriggerRollingUpgrade(&reconciler, context.TODO(), c,
			resourceToReload, value, klogr.New())
		Expect(err).To(BeNil())

		resourceToReload.Kind = "StatefulSet"
		err = controllers.TriggerRollingUpgrade(&reconciler, context.TODO(), c,
			resourceToReload, value, klogr.New())
		Expect(err).To(BeNil())

		resourceToReload.Kind = "DaemonSet"
		err = controllers.TriggerRollingUpgrade(&reconciler, context.TODO(), c,
			resourceToReload, value, klogr.New())
		Expect(err).To(BeNil())
	})

	It("triggerRollingUpgrade updates Deployment containers' env", func() {
		obj, err := utils.GetUnstructured([]byte(depl))
		Expect(err).To(BeNil())
		initObjects := []client.Object{
			obj,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(initObjects...).Build()

		reconciler := controllers.ReloaderReportReconciler{
			Client: c,
		}

		value := randomString()
		resourceToReload := &libsveltosv1alpha1.ReloaderInfo{
			Kind:      "Deployment",
			Namespace: obj.GetNamespace(),
			Name:      obj.GetName(),
		}
		Expect(controllers.TriggerRollingUpgrade(&reconciler, context.TODO(), c, resourceToReload,
			value, klogr.New())).To(BeNil())

		currentDepl := &appsv1.Deployment{}
		Expect(c.Get(context.TODO(),
			types.NamespacedName{Namespace: obj.GetNamespace(), Name: obj.GetName()},
			currentDepl)).To(Succeed())
		Expect(currentDepl.Spec.Template.Spec.Containers).ToNot(BeNil())
		for i := range currentDepl.Spec.Template.Spec.Containers {
			container := &currentDepl.Spec.Template.Spec.Containers[i]
			Expect(container.Env).To(ContainElements(corev1.EnvVar{
				Name:  controllers.SveltosEnv,
				Value: value,
			}))
		}
	})

	It("processReloaderReport deletes ReloaderReport in the management cluster", func() {
		cluster := prepareCluster()

		clusterType := libsveltosv1alpha1.ClusterTypeCapi
		reloaderReport := getReloaderReport(cluster.Namespace, cluster.Name, &clusterType)
		reloaderReport.Namespace = cluster.Namespace
		Expect(testEnv.Create(context.TODO(), reloaderReport)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, reloaderReport)).To(Succeed())

		reconciler := controllers.ReloaderReportReconciler{
			Client: testEnv.Client,
		}

		Expect(controllers.ProcessReloaderReport(&reconciler, context.TODO(),
			reloaderReport, klogr.New())).To(Succeed())

		Eventually(func() bool {
			currentReloaderReport := &libsveltosv1alpha1.ReloaderReport{}
			err := testEnv.Get(context.TODO(),
				types.NamespacedName{Namespace: reloaderReport.Namespace, Name: reloaderReport.Name},
				currentReloaderReport)
			if err == nil {
				return false
			}
			return apierrors.IsNotFound(err)
		}, timeout, pollingInterval).Should(BeTrue())
	})
})

func verifyEnvs(containers []corev1.Container, value string) {
	Expect(containers).ToNot(BeNil())
	for i := range containers {
		Expect(containers[i].Env).To(
			ContainElements(corev1.EnvVar{
				Name:  controllers.SveltosEnv,
				Value: value,
			}))
	}
}
