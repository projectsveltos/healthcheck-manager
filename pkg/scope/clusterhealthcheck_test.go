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

package scope_test

import (
	"context"
	"reflect"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2/klogr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/projectsveltos/healthcheck-manager/pkg/scope"
	libsveltosv1alpha1 "github.com/projectsveltos/libsveltos/api/v1alpha1"
)

const clusterHealthCheckNamePrefix = "scope-"

var _ = Describe("ClusterHealthCheckScope", func() {
	var clusterHealthCheck *libsveltosv1alpha1.ClusterHealthCheck
	var c client.Client

	BeforeEach(func() {
		clusterHealthCheck = &libsveltosv1alpha1.ClusterHealthCheck{
			ObjectMeta: metav1.ObjectMeta{
				Name: clusterHealthCheckNamePrefix + randomString(),
			},
		}
		scheme := setupScheme()
		initObjects := []client.Object{clusterHealthCheck}
		c = fake.NewClientBuilder().WithScheme(scheme).WithObjects(initObjects...).Build()
	})

	It("Return nil,error if ClusterHealthCheck is not specified", func() {
		params := scope.ClusterHealthCheckScopeParams{
			Client: c,
			Logger: klogr.New(),
		}

		scope, err := scope.NewClusterHealthCheckScope(params)
		Expect(err).To(HaveOccurred())
		Expect(scope).To(BeNil())
	})

	It("Return nil,error if client is not specified", func() {
		params := scope.ClusterHealthCheckScopeParams{
			ClusterHealthCheck: clusterHealthCheck,
			Logger:             klogr.New(),
		}

		scope, err := scope.NewClusterHealthCheckScope(params)
		Expect(err).To(HaveOccurred())
		Expect(scope).To(BeNil())
	})

	It("Name returns ClusterHealthCheck Name", func() {
		params := scope.ClusterHealthCheckScopeParams{
			Client:             c,
			ClusterHealthCheck: clusterHealthCheck,
			Logger:             klogr.New(),
		}

		scope, err := scope.NewClusterHealthCheckScope(params)
		Expect(err).ToNot(HaveOccurred())
		Expect(scope).ToNot(BeNil())

		Expect(scope.Name()).To(Equal(clusterHealthCheck.Name))
	})

	It("GetSelector returns ClusterHealthCheck ClusterSelector", func() {
		clusterHealthCheck.Spec.ClusterSelector = libsveltosv1alpha1.Selector("zone=east")
		params := scope.ClusterHealthCheckScopeParams{
			Client:             c,
			ClusterHealthCheck: clusterHealthCheck,
			Logger:             klogr.New(),
		}

		scope, err := scope.NewClusterHealthCheckScope(params)
		Expect(err).ToNot(HaveOccurred())
		Expect(scope).ToNot(BeNil())

		Expect(scope.GetSelector()).To(Equal(string(clusterHealthCheck.Spec.ClusterSelector)))
	})

	It("Close updates ClusterHealthCheck", func() {
		params := scope.ClusterHealthCheckScopeParams{
			Client:             c,
			ClusterHealthCheck: clusterHealthCheck,
			Logger:             klogr.New(),
		}

		scope, err := scope.NewClusterHealthCheckScope(params)
		Expect(err).ToNot(HaveOccurred())
		Expect(scope).ToNot(BeNil())

		clusterHealthCheck.Labels = map[string]string{"clusters": "hr"}
		Expect(scope.Close(context.TODO())).To(Succeed())

		currentClusterHealthCheck := &libsveltosv1alpha1.ClusterHealthCheck{}
		Expect(c.Get(context.TODO(), types.NamespacedName{Name: clusterHealthCheck.Name}, currentClusterHealthCheck)).To(Succeed())
		Expect(currentClusterHealthCheck.Labels).ToNot(BeNil())
		Expect(len(currentClusterHealthCheck.Labels)).To(Equal(1))
		v, ok := currentClusterHealthCheck.Labels["clusters"]
		Expect(ok).To(BeTrue())
		Expect(v).To(Equal("hr"))
	})

	It("SetMatchingClusterRefs sets ClusterHealthCheck.Status.MatchingCluster", func() {
		params := scope.ClusterHealthCheckScopeParams{
			Client:             c,
			ClusterHealthCheck: clusterHealthCheck,
			Logger:             klogr.New(),
		}

		scope, err := scope.NewClusterHealthCheckScope(params)
		Expect(err).ToNot(HaveOccurred())
		Expect(scope).ToNot(BeNil())

		matchingClusters := []corev1.ObjectReference{
			{
				Namespace: "t-" + randomString(),
				Name:      "c-" + randomString(),
			},
		}
		scope.SetMatchingClusterRefs(matchingClusters)
		Expect(reflect.DeepEqual(clusterHealthCheck.Status.MatchingClusterRefs, matchingClusters)).To(BeTrue())
	})

	It("SetClusterConditions updates ClusterHealthCheck Status ClusterConditions", func() {
		params := scope.ClusterHealthCheckScopeParams{
			Client:             c,
			ClusterHealthCheck: clusterHealthCheck,
			Logger:             klogr.New(),
		}

		scope, err := scope.NewClusterHealthCheckScope(params)
		Expect(err).ToNot(HaveOccurred())
		Expect(scope).ToNot(BeNil())

		clusterNamespace := randomString()
		clusterName := randomString()
		hash := []byte(randomString())
		clusterCondition := libsveltosv1alpha1.ClusterCondition{
			ClusterInfo: libsveltosv1alpha1.ClusterInfo{
				Cluster: corev1.ObjectReference{Namespace: clusterNamespace, Name: clusterName},
				Hash:    hash,
			},
		}
		scope.SetClusterConditions([]libsveltosv1alpha1.ClusterCondition{clusterCondition})
		Expect(clusterHealthCheck.Status.ClusterConditions).ToNot(BeNil())
		Expect(len(clusterHealthCheck.Status.ClusterConditions)).To(Equal(1))
		Expect(clusterHealthCheck.Status.ClusterConditions[0].ClusterInfo.Cluster.Namespace).To(Equal(clusterNamespace))
		Expect(clusterHealthCheck.Status.ClusterConditions[0].ClusterInfo.Cluster.Name).To(Equal(clusterName))
		Expect(clusterHealthCheck.Status.ClusterConditions[0].ClusterInfo.Hash).To(Equal(hash))
	})
})
