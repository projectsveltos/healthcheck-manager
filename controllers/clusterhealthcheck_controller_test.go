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

	"github.com/go-logr/logr"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2/textlogger"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/projectsveltos/healthcheck-manager/controllers"
	"github.com/projectsveltos/healthcheck-manager/pkg/scope"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
	libsveltosset "github.com/projectsveltos/libsveltos/lib/set"
)

func getClusterHealthCheckInstance(name, addonLivenessName string) *libsveltosv1beta1.ClusterHealthCheck {
	return &libsveltosv1beta1.ClusterHealthCheck{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: libsveltosv1beta1.ClusterHealthCheckSpec{
			ClusterSelector: libsveltosv1beta1.Selector{
				LabelSelector: metav1.LabelSelector{
					MatchLabels: map[string]string{
						"bar": "foo",
					},
				},
			},
			LivenessChecks: []libsveltosv1beta1.LivenessCheck{
				{
					Type: libsveltosv1beta1.LivenessTypeAddons,
					Name: addonLivenessName,
				},
			},
		},
	}
}

var _ = Describe("ClusterHealthCheck: Reconciler", func() {
	var chc *libsveltosv1beta1.ClusterHealthCheck
	var addonLivenessCheckName string
	var logger logr.Logger

	BeforeEach(func() {
		logger = textlogger.NewLogger(textlogger.NewConfig(textlogger.Verbosity(1)))
		addonLivenessCheckName = randomString()
		chc = getClusterHealthCheckInstance(randomString(), addonLivenessCheckName)
	})

	It("Adds finalizer", func() {
		initObjects := []client.Object{
			chc,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(initObjects...).
			WithObjects(initObjects...).Build()

		reconciler := controllers.ClusterHealthCheckReconciler{
			Client:              c,
			Scheme:              c.Scheme(),
			Mux:                 sync.Mutex{},
			ClusterMap:          make(map[corev1.ObjectReference]*libsveltosset.Set),
			CHCToClusterMap:     make(map[types.NamespacedName]*libsveltosset.Set),
			ClusterHealthChecks: make(map[corev1.ObjectReference]libsveltosv1beta1.Selector),
			HealthCheckMap:      make(map[corev1.ObjectReference]*libsveltosset.Set),
			CHCToHealthCheckMap: make(map[types.NamespacedName]*libsveltosset.Set),
		}
		chcName := client.ObjectKey{
			Name: chc.Name,
		}
		_, err := reconciler.Reconcile(context.TODO(), ctrl.Request{
			NamespacedName: chcName,
		})
		Expect(err).ToNot(HaveOccurred())

		currentChc := &libsveltosv1beta1.ClusterHealthCheck{}
		err = c.Get(context.TODO(), chcName, currentChc)
		Expect(err).ToNot(HaveOccurred())
		Expect(
			controllerutil.ContainsFinalizer(
				currentChc,
				libsveltosv1beta1.ClusterHealthCheckFinalizer,
			),
		).Should(BeTrue())
	})

	It("Remove finalizer", func() {
		Expect(controllerutil.AddFinalizer(chc, libsveltosv1beta1.ClusterHealthCheckFinalizer)).To(BeTrue())

		cluster := &clusterv1.Cluster{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: randomString(),
				Name:      randomString(),
			},
		}

		Expect(addTypeInformationToObject(scheme, cluster)).To(Succeed())

		initObjects := []client.Object{
			chc,
			cluster,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(initObjects...).
			WithObjects(initObjects...).Build()

		chcName := client.ObjectKey{
			Name: chc.Name,
		}

		currentChc := &libsveltosv1beta1.ClusterHealthCheck{}

		Expect(c.Get(context.TODO(), chcName, currentChc)).To(Succeed())
		Expect(c.Delete(context.TODO(), currentChc)).To(Succeed())

		Expect(c.Get(context.TODO(), chcName, currentChc)).To(Succeed())
		currentChc.Status.ClusterConditions = []libsveltosv1beta1.ClusterCondition{
			{
				ClusterInfo: libsveltosv1beta1.ClusterInfo{
					Cluster: corev1.ObjectReference{
						Namespace:  cluster.Namespace,
						Name:       cluster.Name,
						APIVersion: cluster.APIVersion,
						Kind:       cluster.Kind,
					},
					Status: libsveltosv1beta1.SveltosStatusProvisioned,
					Hash:   []byte(randomString()),
				},
			},
		}

		Expect(c.Status().Update(context.TODO(), currentChc)).To(Succeed())

		reconciler := controllers.ClusterHealthCheckReconciler{
			Client:              c,
			Scheme:              c.Scheme(),
			Mux:                 sync.Mutex{},
			ClusterMap:          make(map[corev1.ObjectReference]*libsveltosset.Set),
			CHCToClusterMap:     make(map[types.NamespacedName]*libsveltosset.Set),
			ClusterHealthChecks: make(map[corev1.ObjectReference]libsveltosv1beta1.Selector),
			HealthCheckMap:      make(map[corev1.ObjectReference]*libsveltosset.Set),
			CHCToHealthCheckMap: make(map[types.NamespacedName]*libsveltosset.Set),
		}

		// Because ClusterHealthCheck is currently deployed in a Cluster (Status.ClusterCondition is set
		// indicating that) Reconcile won't be removed Finalizer
		_, err := reconciler.Reconcile(context.TODO(), ctrl.Request{
			NamespacedName: chcName,
		})
		Expect(err).ToNot(HaveOccurred())

		err = c.Get(context.TODO(), chcName, currentChc)
		Expect(err).ToNot(HaveOccurred())
		Expect(controllerutil.ContainsFinalizer(currentChc, libsveltosv1beta1.ClusterHealthCheckFinalizer)).To(BeTrue())

		Expect(c.Get(context.TODO(), chcName, currentChc)).To(Succeed())

		currentChc.Status.ClusterConditions = []libsveltosv1beta1.ClusterCondition{}
		Expect(c.Status().Update(context.TODO(), currentChc)).To(Succeed())

		// Because ClusterHealthCheck is currently deployed nowhere (Status.ClusterCondition is set
		// indicating that) Reconcile will be removed Finalizer
		_, err = reconciler.Reconcile(context.TODO(), ctrl.Request{
			NamespacedName: chcName,
		})
		Expect(err).ToNot(HaveOccurred())

		err = c.Get(context.TODO(), chcName, currentChc)
		Expect(err).To(HaveOccurred())
		Expect(apierrors.IsNotFound(err)).To(BeTrue())
	})

	It("updateClusterConditions updates ClusterHealthCheck Status.ClusterCondition field", func() {
		initObjects := []client.Object{
			chc,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(initObjects...).
			WithObjects(initObjects...).Build()

		currentChc := &libsveltosv1beta1.ClusterHealthCheck{}
		Expect(c.Get(context.TODO(), types.NamespacedName{Name: chc.Name}, currentChc)).To(Succeed())
		currentChc.Status.MatchingClusterRefs = []corev1.ObjectReference{
			{
				Namespace:  randomString(),
				Name:       randomString(),
				Kind:       "Cluster",
				APIVersion: clusterv1.GroupVersion.String(),
			},
			{
				Namespace:  randomString(),
				Name:       randomString(),
				Kind:       libsveltosv1beta1.SveltosClusterKind,
				APIVersion: libsveltosv1beta1.GroupVersion.String(),
			},
		}

		Expect(c.Status().Update(context.TODO(), currentChc)).To(Succeed())

		reconciler := controllers.ClusterHealthCheckReconciler{
			Client:              c,
			Scheme:              c.Scheme(),
			Mux:                 sync.Mutex{},
			ClusterMap:          make(map[corev1.ObjectReference]*libsveltosset.Set),
			CHCToClusterMap:     make(map[types.NamespacedName]*libsveltosset.Set),
			ClusterHealthChecks: make(map[corev1.ObjectReference]libsveltosv1beta1.Selector),
			HealthCheckMap:      make(map[corev1.ObjectReference]*libsveltosset.Set),
			CHCToHealthCheckMap: make(map[types.NamespacedName]*libsveltosset.Set),
		}

		chcScope, err := scope.NewClusterHealthCheckScope(scope.ClusterHealthCheckScopeParams{
			Client:             c,
			Logger:             logger,
			ClusterHealthCheck: currentChc,
			ControllerName:     "classifier",
		})
		Expect(err).To(BeNil())

		controllers.UpdateClusterConditions(&reconciler, context.TODO(), chcScope)
		Expect(chcScope.PatchObject(context.TODO())).To(Succeed())

		Expect(c.Get(context.TODO(), types.NamespacedName{Name: chc.Name}, currentChc)).To(Succeed())
		Expect(currentChc.Status.ClusterConditions).ToNot(BeNil())
		Expect(len(currentChc.Status.ClusterConditions)).To(Equal(len(currentChc.Status.MatchingClusterRefs)))
	})

	It("cleanMaps cleans ClusterHealthCheckReconciler maps", func() {
		initObjects := []client.Object{
			chc,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(initObjects...).
			WithObjects(initObjects...).Build()

		Expect(addTypeInformationToObject(scheme, chc)).To(Succeed())

		reconciler := controllers.ClusterHealthCheckReconciler{
			Client:              c,
			Scheme:              c.Scheme(),
			Mux:                 sync.Mutex{},
			ClusterMap:          make(map[corev1.ObjectReference]*libsveltosset.Set),
			CHCToClusterMap:     make(map[types.NamespacedName]*libsveltosset.Set),
			ClusterHealthChecks: make(map[corev1.ObjectReference]libsveltosv1beta1.Selector),
			HealthCheckMap:      make(map[corev1.ObjectReference]*libsveltosset.Set),
			CHCToHealthCheckMap: make(map[types.NamespacedName]*libsveltosset.Set),
		}
		chcScope, err := scope.NewClusterHealthCheckScope(scope.ClusterHealthCheckScopeParams{
			Client:             c,
			Logger:             logger,
			ClusterHealthCheck: chc,
			ControllerName:     "classifier",
		})
		Expect(err).To(BeNil())

		chcRef := controllers.GetKeyFromObject(scheme, chc)

		clusterInfo := &corev1.ObjectReference{Namespace: randomString(), Name: randomString(),
			Kind: libsveltosv1beta1.SveltosClusterKind, APIVersion: libsveltosv1beta1.GroupVersion.String()}
		controllers.GetClusterMapForEntry(&reconciler, clusterInfo).Insert(chcRef)

		healthCheckInfo := &corev1.ObjectReference{Name: randomString(),
			Kind: libsveltosv1beta1.HealthCheckKind, APIVersion: libsveltosv1beta1.GroupVersion.String()}
		controllers.GetReferenceMapForEntry(&reconciler, healthCheckInfo).Insert(chcRef)

		reconciler.ClusterHealthChecks[*chcRef] = chc.Spec.ClusterSelector

		controllers.CleanMaps(&reconciler, chcScope)

		Expect(len(reconciler.CHCToClusterMap)).To(Equal(0))
		Expect(len(reconciler.CHCToHealthCheckMap)).To(Equal(0))
		Expect(len(reconciler.ClusterHealthChecks)).To(Equal(0))
	})

	It("updateMaps updates ClusterHealthCheckReconciler maps", func() {
		hcName := randomString()

		chc.Spec.LivenessChecks = append(chc.Spec.LivenessChecks, libsveltosv1beta1.LivenessCheck{
			Type: libsveltosv1beta1.LivenessTypeHealthCheck,
			Name: randomString(),
			LivenessSourceRef: &corev1.ObjectReference{
				Name:       hcName,
				Kind:       libsveltosv1beta1.HealthCheckKind,
				APIVersion: libsveltosv1beta1.GroupVersion.String(),
			},
		})

		clusterNamespace := randomString()
		clusterName := randomString()

		chc.Status.MatchingClusterRefs = []corev1.ObjectReference{
			{
				Kind:       libsveltosv1beta1.SveltosClusterKind,
				APIVersion: libsveltosv1beta1.GroupVersion.String(),
				Namespace:  clusterNamespace,
				Name:       clusterName,
			},
		}

		initObjects := []client.Object{
			chc,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(initObjects...).
			WithObjects(initObjects...).Build()

		reconciler := controllers.ClusterHealthCheckReconciler{
			Client:              c,
			Scheme:              c.Scheme(),
			Mux:                 sync.Mutex{},
			ClusterMap:          make(map[corev1.ObjectReference]*libsveltosset.Set),
			CHCToClusterMap:     make(map[types.NamespacedName]*libsveltosset.Set),
			ClusterHealthChecks: make(map[corev1.ObjectReference]libsveltosv1beta1.Selector),
			HealthCheckMap:      make(map[corev1.ObjectReference]*libsveltosset.Set),
			CHCToHealthCheckMap: make(map[types.NamespacedName]*libsveltosset.Set),
		}
		chcScope, err := scope.NewClusterHealthCheckScope(scope.ClusterHealthCheckScopeParams{
			Client:             c,
			Logger:             logger,
			ClusterHealthCheck: chc,
			ControllerName:     "classifier",
		})
		Expect(err).To(BeNil())

		controllers.UpdateMaps(&reconciler, chcScope)

		clusterInfo := &corev1.ObjectReference{Namespace: clusterNamespace, Name: clusterName,
			Kind: libsveltosv1beta1.SveltosClusterKind, APIVersion: libsveltosv1beta1.GroupVersion.String()}
		Expect(controllers.GetClusterMapForEntry(&reconciler, clusterInfo).Len()).To(Equal(1))

		healthCheckInfo := &corev1.ObjectReference{Name: hcName,
			Kind: libsveltosv1beta1.HealthCheckKind, APIVersion: libsveltosv1beta1.GroupVersion.String()}
		Expect(controllers.GetReferenceMapForEntry(&reconciler, healthCheckInfo).Len()).To(Equal(1))
	})
})
