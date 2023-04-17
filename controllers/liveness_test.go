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

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/klog/v2/klogr"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	configv1alpha1 "github.com/projectsveltos/addon-manager/api/v1alpha1"
	"github.com/projectsveltos/healthcheck-manager/controllers"
	libsveltosv1alpha1 "github.com/projectsveltos/libsveltos/api/v1alpha1"
)

var _ = Describe("Liveness", func() {
	It("getConditionStatus returns true when passing is set to true", func() {
		result := controllers.GetConditionStatus(true)
		Expect(result).To(Equal(corev1.ConditionTrue))

		result = controllers.GetConditionStatus(false)
		Expect(result).To(Equal(corev1.ConditionFalse))
	})

	It("areAddonsDeployed returns true when all add-ons are provisioned", func() {
		clusterSummary := configv1alpha1.ClusterSummary{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: randomString(),
				Name:      randomString(),
			},
			Status: configv1alpha1.ClusterSummaryStatus{
				FeatureSummaries: []configv1alpha1.FeatureSummary{
					{
						FeatureID: configv1alpha1.FeatureHelm,
						Status:    configv1alpha1.FeatureStatusProvisioning,
					},
					{
						FeatureID: configv1alpha1.FeatureResources,
						Status:    configv1alpha1.FeatureStatusProvisioned,
					},
				},
			},
		}

		Expect(controllers.AreAddonsDeployed(&clusterSummary)).To(BeFalse())

		clusterSummary.Status = configv1alpha1.ClusterSummaryStatus{
			FeatureSummaries: []configv1alpha1.FeatureSummary{
				{
					FeatureID: configv1alpha1.FeatureHelm,
					Status:    configv1alpha1.FeatureStatusProvisioned,
				},
			},
		}

		Expect(controllers.AreAddonsDeployed(&clusterSummary)).To(BeTrue())
	})

	It("fetchClusterSummaries returns all ClusterSummaries for a given Cluster", func() {
		clusterNamespace := randomString()
		clusterName := randomString()
		clusterType := libsveltosv1alpha1.ClusterTypeCapi

		clusterSummary := configv1alpha1.ClusterSummary{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: clusterNamespace,
				Name:      randomString(),
			},
		}

		initObjects := []client.Object{
			&clusterSummary,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(initObjects...).Build()

		clusterSummaries, err := controllers.FetchClusterSummaries(context.TODO(), c, clusterNamespace, clusterName, clusterType)
		Expect(err).To(BeNil())
		Expect(len(clusterSummaries.Items)).To(Equal(0))

		clusterSummary.Labels = map[string]string{
			configv1alpha1.ClusterTypeLabel: string(clusterType),
		}

		Expect(c.Update(context.TODO(), &clusterSummary)).To(Succeed())

		clusterSummaries, err = controllers.FetchClusterSummaries(context.TODO(), c, clusterNamespace, clusterName, clusterType)
		Expect(err).To(BeNil())
		Expect(len(clusterSummaries.Items)).To(Equal(0))

		clusterSummary.Labels = map[string]string{
			configv1alpha1.ClusterTypeLabel: string(clusterType),
			configv1alpha1.ClusterNameLabel: clusterName,
		}

		Expect(c.Update(context.TODO(), &clusterSummary)).To(Succeed())

		clusterSummaries, err = controllers.FetchClusterSummaries(context.TODO(), c, clusterNamespace, clusterName, clusterType)
		Expect(err).To(BeNil())
		Expect(len(clusterSummaries.Items)).To(Equal(1))
	})

	It("hasLivenessCheckStatusChange returns true when status was never evaluated before and status is different", func() {
		chc := &libsveltosv1alpha1.ClusterHealthCheck{
			ObjectMeta: metav1.ObjectMeta{
				Name: randomString(),
			},
			Status: libsveltosv1alpha1.ClusterHealthCheckStatus{
				ClusterConditions: []libsveltosv1alpha1.ClusterCondition{},
			},
		}

		clusterNamespace := randomString()
		clusterName := randomString()
		clusterType := libsveltosv1alpha1.ClusterTypeSveltos
		livenessCheck := libsveltosv1alpha1.LivenessCheck{
			Name: randomString(),
			Type: libsveltosv1alpha1.LivenessTypeAddons,
		}

		Expect(controllers.HasLivenessCheckStatusChange(chc, clusterNamespace, clusterName, clusterType,
			&livenessCheck, true, "")).To(BeTrue())
		Expect(controllers.HasLivenessCheckStatusChange(chc, clusterNamespace, clusterName, clusterType,
			&livenessCheck, false, "")).To(BeTrue())

		apiVersion, kind := schema.GroupVersionKind{
			Group:   libsveltosv1alpha1.GroupVersion.Group,
			Version: libsveltosv1alpha1.GroupVersion.Version,
			Kind:    libsveltosv1alpha1.SveltosClusterKind,
		}.ToAPIVersionAndKind()

		chc.Status = libsveltosv1alpha1.ClusterHealthCheckStatus{
			ClusterConditions: []libsveltosv1alpha1.ClusterCondition{
				{
					ClusterInfo: libsveltosv1alpha1.ClusterInfo{
						Cluster: corev1.ObjectReference{
							Namespace:  clusterNamespace,
							Name:       clusterName,
							Kind:       kind,
							APIVersion: apiVersion,
						},
					},
					Conditions: []libsveltosv1alpha1.Condition{
						{
							Name:   livenessCheck.Name,
							Type:   libsveltosv1alpha1.ConditionType(controllers.GetConditionType(&livenessCheck)),
							Status: corev1.ConditionFalse,
						},
					},
				},
			},
		}

		Expect(controllers.HasLivenessCheckStatusChange(chc, clusterNamespace, clusterName, clusterType,
			&livenessCheck, true, "")).To(BeTrue())
		Expect(controllers.HasLivenessCheckStatusChange(chc, clusterNamespace, clusterName, clusterType,
			&livenessCheck, false, "")).To(BeFalse())

		chc.Status.ClusterConditions[0].Conditions[0].Status = corev1.ConditionTrue
		Expect(controllers.HasLivenessCheckStatusChange(chc, clusterNamespace, clusterName, clusterType,
			&livenessCheck, true, "")).To(BeFalse())
		Expect(controllers.HasLivenessCheckStatusChange(chc, clusterNamespace, clusterName, clusterType,
			&livenessCheck, false, "")).To(BeTrue())
	})

	It("evaluateLivenessCheckAddOns returns true when add-ons are deployed", func() {
		clusterNamespace := randomString()
		clusterName := randomString()
		clusterType := libsveltosv1alpha1.ClusterTypeCapi

		c := prepareClientWithClusterSummaryAndCHC(clusterNamespace, clusterName, clusterType)

		livenessCheck := libsveltosv1alpha1.LivenessCheck{
			Name: randomString(),
			Type: libsveltosv1alpha1.LivenessTypeAddons,
		}

		chcs := &libsveltosv1alpha1.ClusterHealthCheckList{}
		Expect(c.List(context.TODO(), chcs)).To(Succeed())
		Expect(len(chcs.Items)).To(Equal(1))

		passing, err := controllers.EvaluateLivenessCheckAddOns(context.TODO(), c, clusterNamespace, clusterName, clusterType, &chcs.Items[0],
			&livenessCheck, klogr.New())
		Expect(err).To(BeNil())
		Expect(passing).To(BeTrue())
	})

	It("evaluateLivenessCheck", func() {
		clusterNamespace := randomString()
		clusterName := randomString()
		clusterType := libsveltosv1alpha1.ClusterTypeCapi

		c := prepareClientWithClusterSummaryAndCHC(clusterNamespace, clusterName, clusterType)

		livenessCheck := libsveltosv1alpha1.LivenessCheck{
			Name: randomString(),
			Type: libsveltosv1alpha1.LivenessTypeAddons,
		}

		chcs := &libsveltosv1alpha1.ClusterHealthCheckList{}
		Expect(c.List(context.TODO(), chcs)).To(Succeed())
		Expect(len(chcs.Items)).To(Equal(1))

		statusChanged, passing, _, err := controllers.EvaluateLivenessCheck(context.TODO(), c, clusterNamespace, clusterName, clusterType, &chcs.Items[0],
			&livenessCheck, klogr.New())
		Expect(err).To(BeNil())
		Expect(passing).To(BeTrue())
		Expect(statusChanged).To(BeTrue())
	})
})

// prepareClientWithClusterSummaryAndCHC creates a client with a ClusterSummary and a ClusterHealthCheck.
// ClusterHealthCheck has no conditions set yet and Add-ons liveness check;
// ClusterSummary has provisioned all add-ons
// Cluster API cluster
func prepareClientWithClusterSummaryAndCHC(clusterNamespace, clusterName string, clusterType libsveltosv1alpha1.ClusterType) client.Client {
	cluster := &clusterv1.Cluster{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: clusterNamespace,
			Name:      clusterName,
		},
	}

	clusterSummary := &configv1alpha1.ClusterSummary{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: clusterNamespace,
			Name:      randomString(),
			Labels: map[string]string{
				configv1alpha1.ClusterTypeLabel: string(clusterType),
				configv1alpha1.ClusterNameLabel: clusterName,
			},
		},
		Status: configv1alpha1.ClusterSummaryStatus{
			FeatureSummaries: []configv1alpha1.FeatureSummary{
				{
					FeatureID: configv1alpha1.FeatureHelm,
					Status:    configv1alpha1.FeatureStatusProvisioned,
				},
				{
					FeatureID: configv1alpha1.FeatureResources,
					Status:    configv1alpha1.FeatureStatusProvisioned,
				},
			},
		},
	}

	chc := &libsveltosv1alpha1.ClusterHealthCheck{
		ObjectMeta: metav1.ObjectMeta{
			Name: randomString(),
		},
		Spec: libsveltosv1alpha1.ClusterHealthCheckSpec{
			LivenessChecks: []libsveltosv1alpha1.LivenessCheck{
				{
					Name: randomString(),
					Type: libsveltosv1alpha1.LivenessTypeAddons,
				},
			},
		},
		Status: libsveltosv1alpha1.ClusterHealthCheckStatus{
			MatchingClusterRefs: []corev1.ObjectReference{
				{
					Kind: "Cluster", APIVersion: clusterv1.GroupVersion.String(), Namespace: clusterNamespace, Name: clusterName,
				},
			},
			ClusterConditions: []libsveltosv1alpha1.ClusterCondition{},
		},
	}

	initObjects := []client.Object{
		clusterSummary,
		chc,
		cluster,
	}

	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(initObjects...).Build()
	return c
}

// createSecretWithKubeconfig creates a secret containing kubeconfig to access CAPI cluster.
// Uses testEnv.
func createSecretWithKubeconfig(clusterNamespace, clusterName string) {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: clusterNamespace,
			Name:      clusterName + "-kubeconfig",
		},
		Data: map[string][]byte{
			"data": testEnv.Kubeconfig,
		},
	}

	Expect(testEnv.Create(context.TODO(), secret)).To(Succeed())
	Expect(waitForObject(context.TODO(), testEnv.Client, secret)).To(Succeed())
}
