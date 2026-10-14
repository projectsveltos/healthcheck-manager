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
	"sync"

	"github.com/go-logr/logr"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2/textlogger"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/projectsveltos/healthcheck-manager/controllers"
	"github.com/projectsveltos/healthcheck-manager/pkg/scope"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
	fakedeployer "github.com/projectsveltos/libsveltos/lib/deployer/fake"
	libsveltosset "github.com/projectsveltos/libsveltos/lib/set"
)

var _ = Describe("ClusterHealthCheck: ClusterRefs", func() {
	var logger logr.Logger

	BeforeEach(func() {
		logger = textlogger.NewLogger(textlogger.NewConfig(textlogger.Verbosity(1)))
	})

	It("addClusterRefs adds the referenced clusters and skips duplicates", func() {
		matchingCluster := corev1.ObjectReference{
			APIVersion: libsveltosv1beta1.GroupVersion.String(), Kind: libsveltosv1beta1.SveltosClusterKind,
			Namespace: randomString(), Name: randomString(),
		}
		sveltosCluster := corev1.ObjectReference{
			APIVersion: libsveltosv1beta1.GroupVersion.String(), Kind: libsveltosv1beta1.SveltosClusterKind,
			Namespace: randomString(), Name: randomString(),
		}
		capiCluster := corev1.ObjectReference{
			APIVersion: clusterv1.GroupVersion.String(), Kind: ClusterKind,
			Namespace: randomString(), Name: randomString(),
		}

		// The reference to the cluster already matching carries extra fields and an older API version:
		// it must still be recognized as a duplicate.
		duplicate := matchingCluster
		duplicate.APIVersion = "lib.projectsveltos.io/v1alpha1"
		duplicate.UID = "some-uid"
		duplicate.ResourceVersion = "42"

		olderCAPIVersion := capiCluster
		olderCAPIVersion.APIVersion = "cluster.x-k8s.io/v1beta1"

		clusters := controllers.AddClusterRefs([]corev1.ObjectReference{matchingCluster},
			[]corev1.ObjectReference{duplicate, sveltosCluster, olderCAPIVersion, sveltosCluster}, logger)

		Expect(clusters).To(Equal([]corev1.ObjectReference{matchingCluster, sveltosCluster, capiCluster}))
	})

	It("addClusterRefs ignores references that are not a Sveltos or a CAPI Cluster", func() {
		validRef := corev1.ObjectReference{
			APIVersion: libsveltosv1beta1.GroupVersion.String(), Kind: libsveltosv1beta1.SveltosClusterKind,
			Namespace: randomString(), Name: randomString(),
		}

		invalidRefs := []corev1.ObjectReference{
			// Not a cluster
			{APIVersion: "v1", Kind: "Pod", Namespace: randomString(), Name: randomString()},
			// Right group, wrong kind
			{APIVersion: libsveltosv1beta1.GroupVersion.String(), Kind: libsveltosv1beta1.HealthCheckKind,
				Namespace: randomString(), Name: randomString()},
			// Nothing set apart from the name
			{Name: randomString()},
			// No name
			{APIVersion: libsveltosv1beta1.GroupVersion.String(), Kind: libsveltosv1beta1.SveltosClusterKind,
				Namespace: randomString()},
			// No namespace
			{APIVersion: libsveltosv1beta1.GroupVersion.String(), Kind: libsveltosv1beta1.SveltosClusterKind,
				Name: randomString()},
		}

		// Must not panic: clusterproxy.GetClusterType panics on a reference outside Sveltos/CAPI groups.
		clusters := controllers.AddClusterRefs(nil, append(invalidRefs, validRef), logger)

		Expect(clusters).To(Equal([]corev1.ObjectReference{validRef}))
	})

	It("clusterHealthCheckHash does not depend on the order of ClusterRefs", func() {
		cluster1 := corev1.ObjectReference{
			APIVersion: libsveltosv1beta1.GroupVersion.String(), Kind: libsveltosv1beta1.SveltosClusterKind,
			Namespace: randomString(), Name: randomString(),
		}
		cluster2 := corev1.ObjectReference{
			APIVersion: libsveltosv1beta1.GroupVersion.String(), Kind: libsveltosv1beta1.SveltosClusterKind,
			Namespace: randomString(), Name: randomString(),
		}

		chc := &libsveltosv1beta1.ClusterHealthCheck{
			ObjectMeta: metav1.ObjectMeta{Name: randomString()},
			Spec: libsveltosv1beta1.ClusterHealthCheckSpec{
				ClusterRefs: []corev1.ObjectReference{cluster1, cluster2},
			},
		}
		reordered := chc.DeepCopy()
		reordered.Spec.ClusterRefs = []corev1.ObjectReference{cluster2, cluster1}

		c := fake.NewClientBuilder().WithScheme(scheme).Build()

		hash, err := controllers.ClusterHealthCheckHash(context.TODO(), c, chc, &cluster1)
		Expect(err).To(BeNil())
		reorderedHash, err := controllers.ClusterHealthCheckHash(context.TODO(), c, reordered, &cluster1)
		Expect(err).To(BeNil())
		Expect(reorderedHash).To(Equal(hash))

		// The hash must still change when the list of clusters changes
		reordered.Spec.ClusterRefs = []corev1.ObjectReference{cluster2}
		differentHash, err := controllers.ClusterHealthCheckHash(context.TODO(), c, reordered, &cluster1)
		Expect(err).To(BeNil())
		Expect(differentHash).ToNot(Equal(hash))
	})

	It("processClusterHealthCheck does not fail when a referenced cluster does not exist", func() {
		missingCluster := corev1.ObjectReference{
			APIVersion: libsveltosv1beta1.GroupVersion.String(), Kind: libsveltosv1beta1.SveltosClusterKind,
			Namespace: randomString(), Name: randomString(),
		}

		chc := &libsveltosv1beta1.ClusterHealthCheck{
			ObjectMeta: metav1.ObjectMeta{Name: randomString()},
			Spec: libsveltosv1beta1.ClusterHealthCheckSpec{
				ClusterRefs: []corev1.ObjectReference{missingCluster},
			},
			Status: libsveltosv1beta1.ClusterHealthCheckStatus{
				MatchingClusterRefs: []corev1.ObjectReference{missingCluster},
			},
		}

		initObjects := []client.Object{chc}
		c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(initObjects...).
			WithObjects(initObjects...).Build()

		dep := fakedeployer.GetClient(context.TODO(), logger, testEnv.Client)
		controllers.RegisterFeatures(dep, logger)

		reconciler := controllers.ClusterHealthCheckReconciler{
			Client:              c,
			Deployer:            dep,
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
			ControllerName:     controllerName,
		})
		Expect(err).To(BeNil())

		f := controllers.GetHandlersForFeature(libsveltosv1beta1.FeatureClusterHealthCheck)
		clusterInfo, err := controllers.ProcessClusterHealthCheck(&reconciler, context.TODO(), chcScope,
			&missingCluster, f, logger)
		Expect(err).To(BeNil())

		Expect(clusterInfo).ToNot(BeNil())
		Expect(clusterInfo.Status).To(Equal(libsveltosv1beta1.SveltosStatusProvisioning))
		Expect(clusterInfo.FailureMessage).ToNot(BeNil())
		Expect(*clusterInfo.FailureMessage).To(ContainSubstring("cannot proceed deploying"))

		// Nothing is queued for a cluster that does not exist
		Expect(dep.IsInProgress(missingCluster.Namespace, missingCluster.Name, chc.Name,
			libsveltosv1beta1.FeatureClusterHealthCheck, libsveltosv1beta1.ClusterTypeSveltos, false)).To(BeFalse())
	})
})
