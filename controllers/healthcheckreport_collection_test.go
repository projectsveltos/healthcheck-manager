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
	"strings"

	"github.com/go-logr/logr"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2/textlogger"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/projectsveltos/healthcheck-manager/controllers"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
)

const (
	cmVersionName = "sveltos-agent-version"
)

var _ = Describe("HealthCheck Deployer", func() {
	var healthCheck *libsveltosv1beta1.HealthCheck
	var logger logr.Logger
	var version string

	BeforeEach(func() {
		version = randomString()
		healthCheck = getHealthCheckInstance(randomString())
		logger = textlogger.NewLogger(textlogger.NewConfig(textlogger.Verbosity(1)))

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

	It("removeHealthCheckReports deletes all HealthCheckReport for a given HealthCheck instance", func() {
		healthCheckReport1 := getHealthCheckReport(healthCheck.Name, randomString(), randomString())
		healthCheckReport2 := getHealthCheckReport(healthCheck.Name, randomString(), randomString())
		initObjects := []client.Object{
			healthCheckReport1,
			healthCheckReport2,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(initObjects...).
			WithObjects(initObjects...).Build()

		Expect(controllers.RemoveHealthCheckReports(context.TODO(), c, healthCheck.Name, logger)).To(Succeed())

		healthCheckReportList := &libsveltosv1beta1.HealthCheckReportList{}
		Expect(c.List(context.TODO(), healthCheckReportList)).To(Succeed())
		Expect(len(healthCheckReportList.Items)).To(BeZero())
	})

	It("removeHealthCheckReportsFromCluster deletes all HealthCheckReport for a given cluster instance", func() {
		clusterType := libsveltosv1beta1.ClusterTypeCapi
		clusterNamespace := randomString()
		clusterName := randomString()

		// Create a healthCheckReport from clusterNamespace/clusterName for a random HealthCheck (healthCheckName)
		healthCheckName := randomString()
		healthCheckReport1 := getHealthCheckReport(healthCheckName, clusterNamespace, clusterName)
		healthCheckReport1.Labels = libsveltosv1beta1.GetHealthCheckReportLabels(
			healthCheck.Name, clusterName, &clusterType)

		// Create a healthCheckReport from clusterNamespace/clusterName for a random HealthCheck (healthCheckName)
		healthCheckName = randomString()
		healthCheckReport2 := getHealthCheckReport(healthCheckName, clusterNamespace, clusterName)
		healthCheckReport2.Labels = libsveltosv1beta1.GetHealthCheckReportLabels(
			healthCheck.Name, clusterName, &clusterType)

		initObjects := []client.Object{
			healthCheckReport1,
			healthCheckReport2,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(initObjects...).
			WithObjects(initObjects...).Build()

		Expect(controllers.RemoveHealthCheckReportsFromCluster(context.TODO(), c, clusterNamespace, clusterName,
			clusterType, logger)).To(Succeed())

		healthCheckReportList := &libsveltosv1beta1.HealthCheckReportList{}
		Expect(c.List(context.TODO(), healthCheckReportList)).To(Succeed())
		Expect(len(healthCheckReportList.Items)).To(BeZero())
	})

	It("CollectAndProcessHealthCheckReportsFromCluster collects HealthCheckReports from clusters", func() {
		cluster := prepareCluster()

		// In managed cluster this is the namespace where HealthCheckReports
		// are created
		const healthCheckReportNamespace = "projectsveltos"
		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: healthCheckReportNamespace,
			},
		}
		err := testEnv.Create(context.TODO(), ns)
		if err != nil {
			Expect(apierrors.IsAlreadyExists(err)).To(BeTrue())
		}
		Expect(waitForObject(context.TODO(), testEnv.Client, ns)).To(Succeed())

		healthCheckName := randomString()
		healthCheck := getHealthCheckInstance(healthCheckName)
		Expect(testEnv.Create(context.TODO(), healthCheck)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, healthCheck)).To(Succeed())

		healthCheckReport := getHealthCheckReport(healthCheckName, "", "")
		healthCheckReport.Namespace = healthCheckReportNamespace
		Expect(testEnv.Create(context.TODO(), healthCheckReport)).To(Succeed())

		Expect(waitForObject(context.TODO(), testEnv.Client, healthCheckReport)).To(Succeed())

		Expect(controllers.CollectAndProcessHealthCheckReportsFromCluster(context.TODO(),
			testEnv.Client, getClusterRef(cluster), version, logger)).To(Succeed())

		clusterType := libsveltosv1beta1.ClusterTypeCapi

		validateHealthCheckReports(healthCheckName, cluster, &clusterType)

		// Update HealthCheckReports and validate again
		Expect(controllers.CollectAndProcessHealthCheckReportsFromCluster(context.TODO(),
			testEnv.Client, getClusterRef(cluster), version, logger)).To(Succeed())

		validateHealthCheckReports(healthCheckName, cluster, &clusterType)
	})
})

var _ = Describe("HealthCheckReport Collection", func() {
	version := randomString()

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

	It("buildClustersWithHealthCheck returns all clusters matched by any ClusterHealthCheck", func() {
		cluster1 := corev1.ObjectReference{Namespace: randomString(), Name: randomString(), Kind: "Cluster"}
		cluster2 := corev1.ObjectReference{Namespace: randomString(), Name: randomString(), Kind: "Cluster"}
		cluster3 := corev1.ObjectReference{Namespace: randomString(), Name: randomString(), Kind: "SveltosCluster"}
		cluster4 := corev1.ObjectReference{Namespace: randomString(), Name: randomString(), Kind: "Cluster"}

		chc1 := libsveltosv1beta1.ClusterHealthCheck{
			ObjectMeta: metav1.ObjectMeta{Name: randomString()},
		}
		chc1.Status.MatchingClusterRefs = []corev1.ObjectReference{cluster1, cluster2}

		chc2 := libsveltosv1beta1.ClusterHealthCheck{
			ObjectMeta: metav1.ObjectMeta{Name: randomString()},
		}
		chc2.Status.MatchingClusterRefs = []corev1.ObjectReference{cluster2, cluster3}

		// cluster4 is not referenced by any ClusterHealthCheck

		clusterHealthChecks := &libsveltosv1beta1.ClusterHealthCheckList{
			Items: []libsveltosv1beta1.ClusterHealthCheck{chc1, chc2},
		}

		result := controllers.BuildClustersWithHealthCheck(clusterHealthChecks)
		Expect(result).To(HaveLen(3))
		Expect(result[cluster1]).To(BeTrue())
		Expect(result[cluster2]).To(BeTrue())
		Expect(result[cluster3]).To(BeTrue())
		Expect(result).NotTo(HaveKey(cluster4))
	})

	It("collectAndProcessAllHealthCheckReports distinguishes CAPI and Sveltos clusters with same namespace and name", func() {
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

		healthCheckName := randomString()
		healthCheck := getHealthCheckInstance(healthCheckName)
		Expect(testEnv.Create(context.TODO(), healthCheck)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, healthCheck)).To(Succeed())

		clusterType := libsveltosv1beta1.ClusterTypeCapi
		capiClusterType := strings.ToLower(string(clusterType))
		sveltosClusterType := strings.ToLower(string(libsveltosv1beta1.ClusterTypeSveltos))

		// capiHCR: belongs to the CAPI cluster — its Spec.ClusterName is set
		// so updateHealthCheckReport returns early, and Phase is updated to Processed.
		capiHCR := &libsveltosv1beta1.HealthCheckReport{
			ObjectMeta: metav1.ObjectMeta{
				Name:      randomString(),
				Namespace: capiCluster.Namespace,
				Labels: map[string]string{
					libsveltosv1beta1.HealthCheckNameLabel:              healthCheckName,
					libsveltosv1beta1.HealthCheckReportClusterNameLabel: capiCluster.Name,
					libsveltosv1beta1.HealthCheckReportClusterTypeLabel: capiClusterType,
				},
			},
			Spec: libsveltosv1beta1.HealthCheckReportSpec{
				HealthCheckName:  healthCheckName,
				ClusterNamespace: capiCluster.Namespace,
				ClusterName:      capiCluster.Name,
				ClusterType:      clusterType,
			},
		}
		Expect(testEnv.Create(context.TODO(), capiHCR)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, capiHCR)).To(Succeed())

		// sveltosHCR: same namespace and cluster name as capiCluster but type=sveltos.
		// It must NOT be processed since the sveltos cluster is not in clusterList.
		sveltosHCR := &libsveltosv1beta1.HealthCheckReport{
			ObjectMeta: metav1.ObjectMeta{
				Name:      randomString(),
				Namespace: capiCluster.Namespace,
				Labels: map[string]string{
					libsveltosv1beta1.HealthCheckNameLabel:              healthCheckName,
					libsveltosv1beta1.HealthCheckReportClusterNameLabel: capiCluster.Name,
					libsveltosv1beta1.HealthCheckReportClusterTypeLabel: sveltosClusterType,
				},
			},
			Spec: libsveltosv1beta1.HealthCheckReportSpec{
				HealthCheckName:  healthCheckName,
				ClusterNamespace: capiCluster.Namespace,
				ClusterName:      capiCluster.Name,
				ClusterType:      libsveltosv1beta1.ClusterTypeSveltos,
			},
		}
		Expect(testEnv.Create(context.TODO(), sveltosHCR)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, sveltosHCR)).To(Succeed())

		capiClusterRef := corev1.ObjectReference{
			Namespace:  capiCluster.Namespace,
			Name:       capiCluster.Name,
			Kind:       capiCluster.Kind,
			APIVersion: capiCluster.APIVersion,
		}

		// Only the CAPI cluster is in the cluster list.
		clusterList := []corev1.ObjectReference{capiClusterRef}
		logger := textlogger.NewLogger(textlogger.NewConfig())

		Expect(controllers.CollectAndProcessAllHealthCheckReports(context.TODO(), testEnv.Client,
			clusterList, version, logger)).To(Succeed())

		// capiHCR must eventually have Phase = ReportProcessed.
		Eventually(func() bool {
			current := &libsveltosv1beta1.HealthCheckReport{}
			err := testEnv.Get(context.TODO(),
				types.NamespacedName{Namespace: capiHCR.Namespace, Name: capiHCR.Name}, current)
			return err == nil && current.Status.Phase != nil &&
				*current.Status.Phase == libsveltosv1beta1.ReportProcessed
		}, timeout, pollingInterval).Should(BeTrue())

		// sveltosHCR must consistently NOT be processed (Phase remains nil).
		Consistently(func() bool {
			current := &libsveltosv1beta1.HealthCheckReport{}
			err := testEnv.Get(context.TODO(),
				types.NamespacedName{Namespace: sveltosHCR.Namespace, Name: sveltosHCR.Name}, current)
			return err == nil && (current.Status.Phase == nil ||
				*current.Status.Phase != libsveltosv1beta1.ReportProcessed)
		}, timeout, pollingInterval).Should(BeTrue())
	})
})

func validateHealthCheckReports(healthCheckName string, cluster *clusterv1.Cluster, clusterType *libsveltosv1beta1.ClusterType) {
	// Verify HealthCheckReport is created
	// Eventual loop so testEnv Cache is synced
	Eventually(func() bool {
		healthCheckReportName := libsveltosv1beta1.GetHealthCheckReportName(healthCheckName, cluster.Name, clusterType)
		currentHealthCheckReport := &libsveltosv1beta1.HealthCheckReport{}
		err := testEnv.Get(context.TODO(),
			types.NamespacedName{Namespace: cluster.Namespace, Name: healthCheckReportName}, currentHealthCheckReport)
		if err != nil {
			By("Not found")
			return false
		}
		if currentHealthCheckReport.Labels == nil {
			By("Missing labels")
			return false
		}
		if currentHealthCheckReport.Spec.ClusterNamespace != cluster.Namespace ||
			currentHealthCheckReport.Spec.ClusterName != cluster.Name {

			By("Spec ClusterNamespace and ClusterName not set")
			return false
		}
		v, ok := currentHealthCheckReport.Labels[libsveltosv1beta1.HealthCheckNameLabel]
		return ok && v == healthCheckName
	}, timeout, pollingInterval).Should(BeTrue())
}
