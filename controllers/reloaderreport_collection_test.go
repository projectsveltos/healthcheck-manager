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
	"fmt"
	"reflect"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2/textlogger"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	"github.com/projectsveltos/healthcheck-manager/controllers"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
)

var _ = Describe("ReloaderReport Collection", func() {
	var version string

	BeforeEach(func() {
		version = randomString()

		By("Create the ConfigMap with sveltos-agent version")
		cm := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: controllers.ReportNamespace,
				Name:      cmVersionName,
			},
			Data: map[string]string{
				"version": version,
			},
		}
		Expect(testEnv.Create(context.TODO(), cm)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, cm)).To(Succeed())
	})

	AfterEach(func() {
		By("Delete the ConfigMap with sveltos-agent version")
		cm := &corev1.ConfigMap{}
		Expect(testEnv.Get(context.TODO(),
			types.NamespacedName{Namespace: controllers.ReportNamespace, Name: cmVersionName},
			cm)).To(Succeed())
		Expect(testEnv.Delete(context.TODO(), cm)).To(Succeed())

		Eventually(func() bool {
			err := testEnv.Get(context.TODO(),
				types.NamespacedName{Namespace: controllers.ReportNamespace, Name: cmVersionName},
				cm)
			return err != nil && apierrors.IsNotFound(err)
		}, timeout, pollingInterval).Should(BeTrue())
	})

	It("buildClustersWithReloader returns clusters matched by profiles with Reloader=true", func() {
		cluster1 := corev1.ObjectReference{Namespace: randomString(), Name: randomString(), Kind: "Cluster"}
		cluster2 := corev1.ObjectReference{Namespace: randomString(), Name: randomString(), Kind: "Cluster"}
		cluster3 := corev1.ObjectReference{Namespace: randomString(), Name: randomString(), Kind: "SveltosCluster"}
		cluster4 := corev1.ObjectReference{Namespace: randomString(), Name: randomString(), Kind: "Cluster"}

		// ClusterProfile with Reloader=true matching cluster1 and cluster2.
		cpWithReloader := &configv1beta1.ClusterProfile{
			ObjectMeta: metav1.ObjectMeta{Name: randomString()},
			Spec:       configv1beta1.Spec{Reloader: true},
		}
		// Profile with Reloader=true matching cluster3.
		profileWithReloader := &configv1beta1.Profile{
			ObjectMeta: metav1.ObjectMeta{Name: randomString(), Namespace: randomString()},
			Spec:       configv1beta1.Spec{Reloader: true},
		}
		// ClusterProfile with Reloader=false matching cluster4 — must not appear in result.
		cpNoReloader := &configv1beta1.ClusterProfile{
			ObjectMeta: metav1.ObjectMeta{Name: randomString()},
			Spec:       configv1beta1.Spec{Reloader: false},
		}

		initObjects := []client.Object{cpWithReloader, profileWithReloader, cpNoReloader}
		c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(initObjects...).
			WithObjects(initObjects...).Build()

		// Set Status.MatchingClusterRefs via update (fake client requires status subresource update).
		cpWithReloader.Status.MatchingClusterRefs = []corev1.ObjectReference{cluster1, cluster2}
		Expect(c.Status().Update(context.TODO(), cpWithReloader)).To(Succeed())

		profileWithReloader.Status.MatchingClusterRefs = []corev1.ObjectReference{cluster3}
		Expect(c.Status().Update(context.TODO(), profileWithReloader)).To(Succeed())

		cpNoReloader.Status.MatchingClusterRefs = []corev1.ObjectReference{cluster4}
		Expect(c.Status().Update(context.TODO(), cpNoReloader)).To(Succeed())

		result, err := controllers.BuildClustersWithReloader(context.TODO(), c)
		Expect(err).To(BeNil())
		Expect(result).To(HaveLen(3))
		Expect(result[cluster1]).To(BeTrue())
		Expect(result[cluster2]).To(BeTrue())
		Expect(result[cluster3]).To(BeTrue())
		Expect(result).NotTo(HaveKey(cluster4))
	})

	It("collectAndProcessAllReloaderReports distinguishes CAPI and Sveltos clusters with same namespace and name", func() {
		capiCluster := prepareCluster()

		cmName := fmt.Sprintf("sa-%s-%s", strings.ToLower(string(libsveltosv1beta1.ClusterTypeCapi)), capiCluster.Name)
		cmNamespace := capiCluster.Namespace
		cm := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: cmNamespace,
				Name:      cmName,
			},
			Data: map[string]string{
				"version": version,
			},
		}
		Expect(testEnv.Create(context.TODO(), cm)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, cm)).To(Succeed())

		capiClusterType := libsveltosv1beta1.ClusterTypeCapi
		sveltosClusterType := libsveltosv1beta1.ClusterTypeSveltos

		// capiRR: in the CAPI cluster's namespace with proper cluster labels.
		// Spec.ClusterName is empty so updateReloaderReport will process it.
		capiRR := &libsveltosv1beta1.ReloaderReport{
			ObjectMeta: metav1.ObjectMeta{
				Name:      randomString(),
				Namespace: capiCluster.Namespace,
				Labels:    libsveltosv1beta1.GetReloaderReportLabels(capiCluster.Name, &capiClusterType),
			},
			Spec: libsveltosv1beta1.ReloaderReportSpec{
				ClusterNamespace: capiCluster.Namespace,
				ClusterType:      capiClusterType,
			},
		}
		Expect(testEnv.Create(context.TODO(), capiRR)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, capiRR)).To(Succeed())

		// sveltosRR: same namespace and cluster name as capiCluster but type=sveltos.
		// It must NOT be processed since the sveltos cluster is not in clusterList.
		sveltosRR := &libsveltosv1beta1.ReloaderReport{
			ObjectMeta: metav1.ObjectMeta{
				Name:      randomString(),
				Namespace: capiCluster.Namespace,
				Labels:    libsveltosv1beta1.GetReloaderReportLabels(capiCluster.Name, &sveltosClusterType),
			},
			Spec: libsveltosv1beta1.ReloaderReportSpec{
				ClusterNamespace: capiCluster.Namespace,
				ClusterType:      sveltosClusterType,
			},
		}
		Expect(testEnv.Create(context.TODO(), sveltosRR)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, sveltosRR)).To(Succeed())

		capiClusterRef := corev1.ObjectReference{
			Namespace:  capiCluster.Namespace,
			Name:       capiCluster.Name,
			Kind:       capiCluster.Kind,
			APIVersion: capiCluster.APIVersion,
		}

		// Only the CAPI cluster is in the cluster list.
		clusterList := []corev1.ObjectReference{capiClusterRef}
		logger := textlogger.NewLogger(textlogger.NewConfig())

		Expect(controllers.CollectAndProcessAllReloaderReports(context.TODO(), testEnv.Client,
			clusterList, version, logger)).To(Succeed())

		// capi RR must eventually be deleted (processed and removed by the agentless path).
		Eventually(func() bool {
			current := &libsveltosv1beta1.ReloaderReport{}
			err := testEnv.Get(context.TODO(),
				types.NamespacedName{Namespace: capiRR.Namespace, Name: capiRR.Name}, current)
			return apierrors.IsNotFound(err)
		}, timeout, pollingInterval).Should(BeTrue())

		// sveltos RR must consistently NOT be deleted (not processed).
		Consistently(func() bool {
			current := &libsveltosv1beta1.ReloaderReport{}
			err := testEnv.Get(context.TODO(),
				types.NamespacedName{Namespace: sveltosRR.Namespace, Name: sveltosRR.Name}, current)
			return err == nil
		}, timeout, pollingInterval).Should(BeTrue())

		Expect(testEnv.Delete(context.TODO(), sveltosRR)).To(Succeed())
		Eventually(func() bool {
			current := &libsveltosv1beta1.ReloaderReport{}
			err := testEnv.Get(context.TODO(),
				types.NamespacedName{Namespace: sveltosRR.Namespace, Name: sveltosRR.Name}, current)
			return apierrors.IsNotFound(err)
		}, timeout, pollingInterval).Should(BeTrue())
	})

	It("collectAndProcessReloaderReportsFromCluster collects ReloaderReports from cluster", func() {
		cluster := prepareCluster()

		// In managed cluster this is the namespace where ReloaderReports
		// are created
		const reloaderReportNamespace = "projectsveltos"
		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: reloaderReportNamespace,
			},
		}
		err := testEnv.Create(context.TODO(), ns)
		if err != nil {
			Expect(apierrors.IsAlreadyExists(err)).To(BeTrue())
		}
		Expect(waitForObject(context.TODO(), testEnv.Client, ns)).To(Succeed())

		By("Creating reloaderReport in the managed cluster")
		clusterType := libsveltosv1beta1.ClusterTypeCapi
		reloaderReport := getReloaderReport("", "", &clusterType)
		reloaderReport.Namespace = reloaderReportNamespace
		Expect(testEnv.Create(context.TODO(), reloaderReport)).To(Succeed())

		Expect(waitForObject(context.TODO(), testEnv.Client, reloaderReport)).To(Succeed())

		Expect(controllers.CollectAndProcessReloaderReportsFromCluster(context.TODO(), testEnv.Client,
			getClusterRef(cluster), version,
			textlogger.NewLogger(textlogger.NewConfig(textlogger.Verbosity(1))))).To(Succeed())

		By("Verify ReloaderReport is created in the management cluster")
		// ReloaderReport is fetched from managed cluster (testEnv, projectsveltos namespace)
		// and created in the management cluster (testEnv, cluster namespace)
		validateReloaderReports(cluster, reloaderReport.Spec.ResourcesToReload)

		By("Verify ReloaderReport is deleted from managed cluster")
		// Verify ReloaderReport has been deleted in the managed cluster
		// TestEnv is used for both managed and management cluster. ReloaderReport is in the
		// projectsveltos namespace in the managed cluster. CollectAndProcessReloaderReportsFromCluster
		// removes ReloaderReport from managed cluster after fetching it to management cluster
		Eventually(func() bool {
			currentReloaderReport := &libsveltosv1beta1.ReloaderReport{}
			err = testEnv.Get(context.TODO(),
				types.NamespacedName{Namespace: reloaderReportNamespace, Name: reloaderReport.Name},
				currentReloaderReport)
			if err == nil {
				return false
			}
			return apierrors.IsNotFound(err)
		}, timeout, pollingInterval).Should(BeTrue())

		By("Recreating reloaderReport in the managed cluster")
		// Recreate reloaderReport in the managed cluster
		reloaderReport = getReloaderReport("", "", &clusterType)
		reloaderReport.Namespace = reloaderReportNamespace
		Expect(testEnv.Create(context.TODO(), reloaderReport)).To(Succeed())

		Expect(controllers.CollectAndProcessReloaderReportsFromCluster(context.TODO(), testEnv.Client,
			getClusterRef(cluster), version,
			textlogger.NewLogger(textlogger.NewConfig(textlogger.Verbosity(1))))).To(Succeed())

		validateReloaderReports(cluster, reloaderReport.Spec.ResourcesToReload)

		By("Verify ReloaderReport is deleted from managed cluster")
		// Verify ReloaderReport has been deleted in the managed cluster
		// TestEnv is used for both managed and management cluster. ReloaderReport is in the
		// projectsveltos namespace in the managed cluster. CollectAndProcessReloaderReportsFromCluster
		// removes ReloaderReport from managed cluster after fetching it to management cluster
		Eventually(func() bool {
			currentReloaderReport := &libsveltosv1beta1.ReloaderReport{}
			err = testEnv.Get(context.TODO(),
				types.NamespacedName{Namespace: reloaderReportNamespace, Name: reloaderReport.Name},
				currentReloaderReport)
			if err == nil {
				return false
			}
			return apierrors.IsNotFound(err)
		}, timeout, pollingInterval).Should(BeTrue())
	})
})

func validateReloaderReports(cluster *clusterv1.Cluster, expectedReloaderInfo []libsveltosv1beta1.ReloaderInfo) {
	// Verify ReloaderReport is created in the cluster namespace
	// Eventual loop so testEnv Cache is synced
	Eventually(func() bool {
		listOptions := []client.ListOption{
			client.InNamespace(cluster.Namespace),
		}

		reloaderReports := &libsveltosv1beta1.ReloaderReportList{}
		err := testEnv.List(context.TODO(), reloaderReports, listOptions...)
		if err != nil {
			return false
		}

		filtered := []libsveltosv1beta1.ReloaderReport{}
		// filter reports that match the cluster
		for i := range reloaderReports.Items {
			if !reloaderReports.Items[i].DeletionTimestamp.IsZero() {
				continue
			}

			if reloaderReports.Items[i].Spec.ClusterNamespace == cluster.Namespace &&
				reloaderReports.Items[i].Spec.ClusterName == cluster.Name &&
				reloaderReports.Items[i].Spec.ClusterType == libsveltosv1beta1.ClusterTypeCapi {

				filtered = append(filtered, reloaderReports.Items[i])
			}
		}

		if len(filtered) != 1 {
			By(fmt.Sprintf("found %d reloaderReports", len(filtered)))
			return false
		}
		if !reflect.DeepEqual(filtered[0].Spec.ResourcesToReload, expectedReloaderInfo) {
			By("ReloaderInfo does not match")
			return false
		}
		return true
	}, timeout, pollingInterval).Should(BeTrue())
}
