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

package controllers_test

import (
	"context"

	"github.com/go-logr/logr"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2/textlogger"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/projectsveltos/healthcheck-manager/controllers"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
	"github.com/projectsveltos/libsveltos/lib/mgmtagent"
)

const existingValue = "other"

var _ = Describe("HealthCheck ConfigMap used when sveltos-agent runs in the management cluster", func() {
	var logger logr.Logger
	var clusterNamespace, clusterName string
	var chc *libsveltosv1beta1.ClusterHealthCheck
	var healthCheck *libsveltosv1beta1.HealthCheck

	BeforeEach(func() {
		logger = textlogger.NewLogger(textlogger.NewConfig(textlogger.Verbosity(1)))
		clusterNamespace = randomString()
		clusterName = randomString()
		chc = &libsveltosv1beta1.ClusterHealthCheck{ObjectMeta: metav1.ObjectMeta{Name: randomString()}}
		healthCheck = &libsveltosv1beta1.HealthCheck{ObjectMeta: metav1.ObjectMeta{Name: randomString()}}
	})

	getConfigMap := func(c client.Client) *corev1.ConfigMap {
		configMap := &corev1.ConfigMap{}
		Expect(c.Get(context.TODO(),
			types.NamespacedName{
				Namespace: clusterNamespace,
				Name:      mgmtagent.GetConfigMapName(clusterName, libsveltosv1beta1.ClusterTypeCapi),
			}, configMap)).To(Succeed())
		return configMap
	}

	It("addHealthCheckToConfigMap creates the ConfigMap when it does not exist", func() {
		c := fake.NewClientBuilder().WithScheme(scheme).Build()

		Expect(controllers.AddHealthCheckToConfigMap(context.TODO(), c, clusterNamespace, clusterName,
			libsveltosv1beta1.ClusterTypeCapi, chc, healthCheck, logger)).To(Succeed())

		key := mgmtagent.GetKeyForHealthCheck(chc.Name, healthCheck.Name)
		Expect(getConfigMap(c).Data).To(Equal(map[string]string{key: healthCheck.Name}))
	})

	It("addHealthCheckToConfigMap adds an entry to an existing ConfigMap", func() {
		existingKey := mgmtagent.GetKeyForHealthCheck(randomString(), randomString())
		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
			existingConfigMap(clusterNamespace, clusterName, existingKey, existingValue)).Build()

		Expect(controllers.AddHealthCheckToConfigMap(context.TODO(), c, clusterNamespace, clusterName,
			libsveltosv1beta1.ClusterTypeCapi, chc, healthCheck, logger)).To(Succeed())

		key := mgmtagent.GetKeyForHealthCheck(chc.Name, healthCheck.Name)
		Expect(getConfigMap(c).Data).To(Equal(map[string]string{existingKey: existingValue, key: healthCheck.Name}))
	})

	It("addHealthCheckToConfigMap adds the entry when the ConfigMap is created by someone else after it was read", func() {
		existingKey := mgmtagent.GetKeyForHealthCheck(randomString(), randomString())
		configMap := existingConfigMap(clusterNamespace, clusterName, existingKey, existingValue)

		// The ConfigMap exists, but reading it reports NotFound (stale cache or concurrent creation)
		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(configMap).
			WithInterceptorFuncs(interceptor.Funcs{
				Get: func(_ context.Context, _ client.WithWatch, key client.ObjectKey, obj client.Object,
					_ ...client.GetOption) error {

					if _, ok := obj.(*corev1.ConfigMap); ok && key.Name == configMap.Name {
						return apierrors.NewNotFound(schema.GroupResource{Resource: "configmaps"}, key.Name)
					}
					return nil
				},
			}).Build()

		Expect(controllers.AddHealthCheckToConfigMap(context.TODO(), c, clusterNamespace, clusterName,
			libsveltosv1beta1.ClusterTypeCapi, chc, healthCheck, logger)).To(Succeed())

		// Read the ConfigMap bypassing the interceptor
		list := &corev1.ConfigMapList{}
		Expect(c.List(context.TODO(), list, client.InNamespace(clusterNamespace))).To(Succeed())
		Expect(list.Items).To(HaveLen(1))
		key := mgmtagent.GetKeyForHealthCheck(chc.Name, healthCheck.Name)
		Expect(list.Items[0].Data).To(Equal(map[string]string{existingKey: existingValue, key: healthCheck.Name}))
	})
})

func existingConfigMap(clusterNamespace, clusterName, key, value string) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: clusterNamespace,
			Name:      mgmtagent.GetConfigMapName(clusterName, libsveltosv1beta1.ClusterTypeCapi),
		},
		Data: map[string]string{key: value},
	}
}
