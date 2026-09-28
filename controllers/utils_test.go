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
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"

	"github.com/projectsveltos/healthcheck-manager/controllers"
)

var _ = Describe("Utils", func() {
	It("InitScheme registers ConfigMap, Cluster, ClusterProfile, HealthCheck and CustomResourceDefinition", func() {
		s, err := controllers.InitScheme()
		Expect(err).To(BeNil())

		Expect(s.Recognizes(corev1.SchemeGroupVersion.WithKind("ConfigMap"))).To(BeTrue())
		Expect(s.Recognizes(corev1.SchemeGroupVersion.WithKind("Secret"))).To(BeTrue())
		Expect(s.Recognizes(clusterv1.GroupVersion.WithKind("Cluster"))).To(BeTrue())
		Expect(s.Recognizes(configv1beta1.GroupVersion.WithKind(configv1beta1.ClusterProfileKind))).To(BeTrue())
		Expect(s.Recognizes(libsveltosv1beta1.GroupVersion.WithKind(libsveltosv1beta1.HealthCheckKind))).To(BeTrue())
		Expect(s.Recognizes(libsveltosv1beta1.GroupVersion.WithKind(libsveltosv1beta1.HealthCheckReportKind))).To(BeTrue())
		Expect(s.Recognizes(apiextensionsv1.SchemeGroupVersion.WithKind("CustomResourceDefinition"))).To(BeTrue())
	})

	It("GetKeyFromObject stamps kind and apiVersion for a HealthCheck", func() {
		s, err := controllers.InitScheme()
		Expect(err).To(BeNil())

		healthCheck := &libsveltosv1beta1.HealthCheck{
			ObjectMeta: metav1.ObjectMeta{
				Name: randomString(),
			},
		}

		ref := controllers.GetKeyFromObject(s, healthCheck)
		Expect(ref).ToNot(BeNil())
		Expect(ref.Name).To(Equal(healthCheck.Name))
		Expect(ref.Namespace).To(Equal(healthCheck.Namespace))
		Expect(ref.Kind).To(Equal(libsveltosv1beta1.HealthCheckKind))
		Expect(ref.APIVersion).To(Equal(libsveltosv1beta1.GroupVersion.String()))
	})

	It("GetKeyFromObject stamps kind and apiVersion for a namespaced ConfigMap", func() {
		s, err := controllers.InitScheme()
		Expect(err).To(BeNil())

		configMap := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: randomString(),
				Name:      randomString(),
			},
		}

		ref := controllers.GetKeyFromObject(s, configMap)
		Expect(ref).ToNot(BeNil())
		Expect(ref.Namespace).To(Equal(configMap.Namespace))
		Expect(ref.Name).To(Equal(configMap.Name))
		Expect(ref.Kind).To(Equal("ConfigMap"))
		Expect(ref.APIVersion).To(Equal(corev1.SchemeGroupVersion.String()))
	})

	It("GetKeyFromObject returns different keys for objects of different kinds", func() {
		s, err := controllers.InitScheme()
		Expect(err).To(BeNil())

		name := randomString()
		namespace := randomString()

		configMap := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
		}
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
		}

		configMapRef := controllers.GetKeyFromObject(s, configMap)
		secretRef := controllers.GetKeyFromObject(s, secret)

		Expect(configMapRef.Kind).ToNot(Equal(secretRef.Kind))
		Expect(configMapRef.Name).To(Equal(secretRef.Name))
		Expect(configMapRef.Namespace).To(Equal(secretRef.Namespace))
	})
})
