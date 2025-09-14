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
	"sync"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta1" //nolint:staticcheck // SA1019: We are unable to update the dependency at this time.
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/projectsveltos/healthcheck-manager/controllers"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
	libsveltosset "github.com/projectsveltos/libsveltos/lib/set"
)

var _ = Describe("ClusterHealthCheckReconciler map functions", func() {
	var namespace string

	const upstreamClusterNamePrefix = "transformation-"

	BeforeEach(func() {
		namespace = "map-function" + randomString()
	})

	It("requeueClusterHealthCheckForCluster returns matching ClusterHealthChecks", func() {
		cluster := &clusterv1.Cluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      upstreamClusterNamePrefix + randomString(),
				Namespace: namespace,
				Labels: map[string]string{
					"env": "production",
				},
			},
		}

		matchingClusterHealthCheck := &libsveltosv1beta1.ClusterHealthCheck{
			ObjectMeta: metav1.ObjectMeta{
				Name: upstreamClusterNamePrefix + randomString(),
			},
			Spec: libsveltosv1beta1.ClusterHealthCheckSpec{
				ClusterSelector: libsveltosv1beta1.Selector{
					LabelSelector: metav1.LabelSelector{
						MatchLabels: map[string]string{
							"env": "production",
						},
					},
				},
			},
		}

		nonMatchingClusterHealthCheck := &libsveltosv1beta1.ClusterHealthCheck{
			ObjectMeta: metav1.ObjectMeta{
				Name: upstreamClusterNamePrefix + randomString(),
			},
			Spec: libsveltosv1beta1.ClusterHealthCheckSpec{
				ClusterSelector: libsveltosv1beta1.Selector{
					LabelSelector: metav1.LabelSelector{
						MatchLabels: map[string]string{
							"env": "qa",
						},
					},
				},
			},
		}

		initObjects := []client.Object{
			matchingClusterHealthCheck,
			nonMatchingClusterHealthCheck,
			cluster,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(initObjects...).
			WithObjects(initObjects...).Build()

		reconciler := &controllers.ClusterHealthCheckReconciler{
			Client:              c,
			Scheme:              scheme,
			ClusterMap:          make(map[corev1.ObjectReference]*libsveltosset.Set),
			CHCToClusterMap:     make(map[types.NamespacedName]*libsveltosset.Set),
			ClusterHealthChecks: make(map[corev1.ObjectReference]libsveltosv1beta1.Selector),
			HealthCheckMap:      make(map[corev1.ObjectReference]*libsveltosset.Set),
			CHCToHealthCheckMap: make(map[types.NamespacedName]*libsveltosset.Set),
			ClusterLabels:       make(map[corev1.ObjectReference]map[string]string),
			Mux:                 sync.Mutex{},
		}

		By("Setting ClusterHealthCheckReconciler internal structures")
		matchingInfo := corev1.ObjectReference{APIVersion: cluster.APIVersion,
			Kind: libsveltosv1beta1.ClusterHealthCheckKind, Name: matchingClusterHealthCheck.Name}
		reconciler.ClusterHealthChecks[matchingInfo] = matchingClusterHealthCheck.Spec.ClusterSelector
		nonMatchingInfo := corev1.ObjectReference{APIVersion: cluster.APIVersion,
			Kind: libsveltosv1beta1.ClusterHealthCheckKind, Name: nonMatchingClusterHealthCheck.Name}
		reconciler.ClusterHealthChecks[nonMatchingInfo] = nonMatchingClusterHealthCheck.Spec.ClusterSelector

		// ClusterMap contains, per ClusterName, list of ClusterHealthChecks matching it.
		clusterHealthCheckSet := &libsveltosset.Set{}
		clusterHealthCheckSet.Insert(&matchingInfo)
		clusterInfo := corev1.ObjectReference{APIVersion: cluster.APIVersion, Kind: cluster.Kind,
			Namespace: cluster.Namespace, Name: cluster.Name}
		reconciler.ClusterMap[clusterInfo] = clusterHealthCheckSet

		// CHCToClusterMap contains, per ClusterHealthCheck, list of matched Clusters.
		clusterSet1 := &libsveltosset.Set{}
		reconciler.CHCToClusterMap[types.NamespacedName{Name: nonMatchingInfo.Name}] = clusterSet1

		clusterSet2 := &libsveltosset.Set{}
		clusterSet2.Insert(&clusterInfo)
		reconciler.CHCToClusterMap[types.NamespacedName{Name: matchingInfo.Name}] = clusterSet2

		By("Expect only matchingClusterHealthCheck to be requeued")
		requests := controllers.RequeueClusterHealthCheckForCluster(reconciler, context.TODO(), cluster)
		expected := reconcile.Request{NamespacedName: types.NamespacedName{Name: matchingClusterHealthCheck.Name}}
		Expect(requests).To(ContainElement(expected))

		By("Changing clusterHealthCheck ClusterSelector again to have two ClusterHealthChecks match")
		nonMatchingClusterHealthCheck.Spec.ClusterSelector = matchingClusterHealthCheck.Spec.ClusterSelector
		Expect(c.Update(context.TODO(), nonMatchingClusterHealthCheck)).To(Succeed())

		reconciler.ClusterHealthChecks[nonMatchingInfo] = nonMatchingClusterHealthCheck.Spec.ClusterSelector

		clusterSet1.Insert(&clusterInfo)
		reconciler.CHCToClusterMap[types.NamespacedName{Name: nonMatchingInfo.Name}] = clusterSet1

		clusterHealthCheckSet.Insert(&nonMatchingInfo)
		reconciler.ClusterMap[clusterInfo] = clusterHealthCheckSet

		requests = controllers.RequeueClusterHealthCheckForCluster(reconciler, context.TODO(), cluster)
		expected = reconcile.Request{NamespacedName: types.NamespacedName{Name: matchingClusterHealthCheck.Name}}
		Expect(requests).To(ContainElement(expected))
		expected = reconcile.Request{NamespacedName: types.NamespacedName{Name: nonMatchingClusterHealthCheck.Name}}
		Expect(requests).To(ContainElement(expected))

		By("Changing clusterHealthCheck ClusterSelector again to have no ClusterHealthCheck match")
		matchingClusterHealthCheck.Spec.ClusterSelector = libsveltosv1beta1.Selector{
			LabelSelector: metav1.LabelSelector{
				MatchLabels: map[string]string{
					"env": "qa",
				},
			},
		}
		Expect(c.Update(context.TODO(), matchingClusterHealthCheck)).To(Succeed())
		nonMatchingClusterHealthCheck.Spec.ClusterSelector = matchingClusterHealthCheck.Spec.ClusterSelector
		Expect(c.Update(context.TODO(), nonMatchingClusterHealthCheck)).To(Succeed())

		emptySet := &libsveltosset.Set{}
		reconciler.CHCToClusterMap[types.NamespacedName{Name: matchingInfo.Name}] = emptySet
		reconciler.CHCToClusterMap[types.NamespacedName{Name: nonMatchingInfo.Name}] = emptySet
		reconciler.ClusterMap[clusterInfo] = emptySet

		reconciler.ClusterHealthChecks[matchingInfo] = matchingClusterHealthCheck.Spec.ClusterSelector
		reconciler.ClusterHealthChecks[nonMatchingInfo] = nonMatchingClusterHealthCheck.Spec.ClusterSelector

		requests = controllers.RequeueClusterHealthCheckForCluster(reconciler, context.TODO(), cluster)
		Expect(requests).To(HaveLen(0))
	})

	It("RequeueClusterHealthCheckForMachine returns correct ClusterHealthChecks for a CAPI machine", func() {
		cluster := &clusterv1.Cluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      upstreamClusterNamePrefix + randomString(),
				Namespace: namespace,
				Labels: map[string]string{
					"env": "production",
				},
			},
		}

		cpMachine := &clusterv1.Machine{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: cluster.Namespace,
				Name:      cluster.Name + randomString(),
				Labels: map[string]string{
					clusterv1.ClusterNameLabel:         cluster.Name,
					clusterv1.MachineControlPlaneLabel: "ok",
				},
			},
		}

		clusterHealthCheck := &libsveltosv1beta1.ClusterHealthCheck{
			ObjectMeta: metav1.ObjectMeta{
				Name: upstreamClusterNamePrefix + randomString(),
			},
			Spec: libsveltosv1beta1.ClusterHealthCheckSpec{
				ClusterSelector: libsveltosv1beta1.Selector{
					LabelSelector: metav1.LabelSelector{
						MatchLabels: map[string]string{
							"env": "production",
						},
					},
				},
			},
		}

		Expect(addTypeInformationToObject(scheme, cluster)).To(Succeed())
		Expect(addTypeInformationToObject(scheme, cpMachine)).To(Succeed())
		Expect(addTypeInformationToObject(scheme, clusterHealthCheck)).To(Succeed())

		// In this scenario:
		// - ClusterHealthCheck added first
		// - Cluster matching ClusterHealthCheck added later
		// - First controlplane Machine in Cluster is ready
		// The only information Sveltos has are:
		// - Cluster's labels (stored in ClusterLabels map)
		// - ClusterHealthCheck's selector (stored in ClusterHealthChecks maps)
		// RequeueClusterHealthCheckForMachine gets cluster from machine and using ClusterLabels
		// and ClusterHealthChecks maps finds the ClusterHealthChecks that need to be reconciled

		apiVersion, kind := cluster.GetObjectKind().GroupVersionKind().ToAPIVersionAndKind()
		clusterHealthCheckReconciler := getClusterHealthCheckReconciler(testEnv.Client)

		clusterInfo := corev1.ObjectReference{APIVersion: apiVersion, Kind: kind,
			Namespace: cluster.GetNamespace(), Name: cluster.GetName()}
		clusterHealthCheckReconciler.ClusterLabels[clusterInfo] = cluster.Labels

		apiVersion, kind = clusterHealthCheck.GetObjectKind().GroupVersionKind().ToAPIVersionAndKind()
		clusterHealthCheckInfo := corev1.ObjectReference{APIVersion: apiVersion, Kind: kind, Name: clusterHealthCheck.GetName()}
		clusterHealthCheckReconciler.ClusterHealthChecks[clusterHealthCheckInfo] = clusterHealthCheck.Spec.ClusterSelector

		clusterHealthCheckList := controllers.RequeueClusterHealthCheckForMachine(clusterHealthCheckReconciler,
			context.TODO(), cpMachine)
		Expect(len(clusterHealthCheckList)).To(Equal(1))
	})
})
