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
	"github.com/projectsveltos/libsveltos/lib/pullmode"
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
				Namespace: sveltosNamespace,
				Name:      cmVersionName,
			},
			Data: map[string]string{
				cmVersionKey: version,
			},
		}
		Expect(testEnv.Create(context.TODO(), cm)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, cm)).To(Succeed())
	})

	AfterEach(func() {
		By("Delete the ConfigMap with sveltos-agent version")
		cm := &corev1.ConfigMap{}
		Expect(testEnv.Get(context.TODO(),
			types.NamespacedName{Namespace: sveltosNamespace, Name: cmVersionName},
			cm)).To(Succeed())
		Expect(testEnv.Delete(context.TODO(), cm)).To(Succeed())

		Eventually(func() bool {
			err := testEnv.Get(context.TODO(),
				types.NamespacedName{Namespace: sveltosNamespace, Name: cmVersionName},
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
		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: sveltosNamespace,
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
		healthCheckReport.Namespace = sveltosNamespace
		Expect(testEnv.Create(context.TODO(), healthCheckReport)).To(Succeed())

		Expect(waitForObject(context.TODO(), testEnv.Client, healthCheckReport)).To(Succeed())

		clusterRef := getClusterRef(cluster)
		clusters := []corev1.ObjectReference{*clusterRef}
		clusterType := libsveltosv1beta1.ClusterTypeCapi

		By("No ClusterHealthCheck needs the report: it is not copied to the management cluster")
		nothingWanted := controllers.BuildWantedReports(&libsveltosv1beta1.ClusterHealthCheckList{}, clusters)
		Expect(controllers.CollectAndProcessHealthCheckReportsFromCluster(context.TODO(),
			testEnv.Client, clusterRef, nothingWanted, version, logger)).To(Succeed())

		healthCheckReportName := libsveltosv1beta1.GetHealthCheckReportName(healthCheckName, cluster.Name, &clusterType)
		Consistently(func() bool {
			err := testEnv.Get(context.TODO(),
				types.NamespacedName{Namespace: cluster.Namespace, Name: healthCheckReportName},
				&libsveltosv1beta1.HealthCheckReport{})
			return err != nil && apierrors.IsNotFound(err)
		}, "3s", pollingInterval).Should(BeTrue())

		By("A ClusterHealthCheck matching the cluster references the HealthCheck: the report is copied")
		chc := getClusterHealthCheckForHealthCheck(healthCheckName, clusters...)
		wanted := controllers.BuildWantedReports(
			&libsveltosv1beta1.ClusterHealthCheckList{Items: []libsveltosv1beta1.ClusterHealthCheck{*chc}}, clusters)
		Expect(controllers.CollectAndProcessHealthCheckReportsFromCluster(context.TODO(),
			testEnv.Client, clusterRef, wanted, version, logger)).To(Succeed())

		validateHealthCheckReports(healthCheckName, cluster, &clusterType)

		// Update HealthCheckReports and validate again
		Expect(controllers.CollectAndProcessHealthCheckReportsFromCluster(context.TODO(),
			testEnv.Client, clusterRef, wanted, version, logger)).To(Succeed())

		validateHealthCheckReports(healthCheckName, cluster, &clusterType)
	})

	It("CollectAndProcessHealthCheckReportsFromCluster skips without error when cluster no longer exists", func() {
		clusterRef := &corev1.ObjectReference{
			Namespace:  randomString(),
			Name:       randomString(),
			Kind:       libsveltosv1beta1.SveltosClusterKind,
			APIVersion: libsveltosv1beta1.GroupVersion.String(),
		}

		c := fake.NewClientBuilder().WithScheme(scheme).Build()

		wanted := controllers.BuildWantedReports(&libsveltosv1beta1.ClusterHealthCheckList{}, nil)
		Expect(controllers.CollectAndProcessHealthCheckReportsFromCluster(context.TODO(),
			c, clusterRef, wanted, version, logger)).To(Succeed())
	})

	It("isAgentHeartbeatCurrent returns false when agent heartbeat has timed out", func() {
		sveltosCluster := &libsveltosv1beta1.SveltosCluster{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: randomString(),
				Name:      randomString(),
			},
			Spec: libsveltosv1beta1.SveltosClusterSpec{
				PullMode: true,
			},
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(sveltosCluster).
			WithStatusSubresource(sveltosCluster).Build()

		heartbeatTimeout := &pullmode.AgentHeartbeatTimeoutError{}
		failureMessage := heartbeatTimeout.Error()
		sveltosCluster.Status.FailureMessage = &failureMessage
		Expect(c.Status().Update(context.TODO(), sveltosCluster)).To(Succeed())

		clusterRef := &corev1.ObjectReference{
			Namespace:  sveltosCluster.Namespace,
			Name:       sveltosCluster.Name,
			Kind:       libsveltosv1beta1.SveltosClusterKind,
			APIVersion: libsveltosv1beta1.GroupVersion.String(),
		}

		healthy, err := controllers.IsAgentHeartbeatCurrent(context.TODO(), c, clusterRef, logger)
		Expect(err).To(BeNil())
		Expect(healthy).To(BeFalse())
	})

	It("isAgentHeartbeatCurrent returns true when agent heartbeat is current", func() {
		sveltosCluster := &libsveltosv1beta1.SveltosCluster{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: randomString(),
				Name:      randomString(),
			},
			Spec: libsveltosv1beta1.SveltosClusterSpec{
				PullMode: true,
			},
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(sveltosCluster).
			WithStatusSubresource(sveltosCluster).Build()

		clusterRef := &corev1.ObjectReference{
			Namespace:  sveltosCluster.Namespace,
			Name:       sveltosCluster.Name,
			Kind:       libsveltosv1beta1.SveltosClusterKind,
			APIVersion: libsveltosv1beta1.GroupVersion.String(),
		}

		healthy, err := controllers.IsAgentHeartbeatCurrent(context.TODO(), c, clusterRef, logger)
		Expect(err).To(BeNil())
		Expect(healthy).To(BeTrue())
	})
})

var _ = Describe("HealthCheckReport Collection", func() {
	version := randomString()

	BeforeEach(func() {
		version = randomString()

		By("Create the ConfigMap with sveltos-agent version")
		cm := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: sveltosNamespace,
				Name:      cmVersionName,
			},
			Data: map[string]string{
				cmVersionKey: version,
			},
		}
		Expect(testEnv.Create(context.TODO(), cm)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, cm)).To(Succeed())
	})

	AfterEach(func() {
		By("Delete the ConfigMap with sveltos-agent version")
		cm := &corev1.ConfigMap{}
		Expect(testEnv.Get(context.TODO(),
			types.NamespacedName{Namespace: sveltosNamespace, Name: cmVersionName},
			cm)).To(Succeed())
		Expect(testEnv.Delete(context.TODO(), cm)).To(Succeed())

		Eventually(func() bool {
			err := testEnv.Get(context.TODO(),
				types.NamespacedName{Namespace: sveltosNamespace, Name: cmVersionName},
				cm)
			return err != nil && apierrors.IsNotFound(err)
		}, timeout, pollingInterval).Should(BeTrue())
	})

	It("buildClustersWithHealthCheck returns all clusters matched by any ClusterHealthCheck", func() {
		cluster1 := corev1.ObjectReference{Namespace: randomString(), Name: randomString(), Kind: ClusterKind}
		cluster2 := corev1.ObjectReference{Namespace: randomString(), Name: randomString(), Kind: ClusterKind}
		cluster3 := corev1.ObjectReference{Namespace: randomString(), Name: randomString(), Kind: libsveltosv1beta1.SveltosClusterKind}
		cluster4 := corev1.ObjectReference{Namespace: randomString(), Name: randomString(), Kind: ClusterKind}

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

	It("buildWantedReports only keeps the HealthChecks of ClusterHealthChecks matching shard-local clusters", func() {
		shardCluster := corev1.ObjectReference{Namespace: randomString(), Name: randomString(),
			Kind: ClusterKind, APIVersion: clusterv1.GroupVersion.String()}
		otherShardCluster := corev1.ObjectReference{Namespace: randomString(), Name: randomString(),
			Kind: ClusterKind, APIVersion: clusterv1.GroupVersion.String()}
		healthCheckName := randomString()

		active := getClusterHealthCheckForHealthCheck(healthCheckName, shardCluster, otherShardCluster)

		deleting := getClusterHealthCheckForHealthCheck(randomString(), shardCluster)
		now := metav1.Now()
		deleting.DeletionTimestamp = &now
		deleting.Finalizers = []string{randomString()}

		addonsOnly := getClusterHealthCheckForHealthCheck(randomString(), shardCluster)
		addonsOnly.Spec.LivenessChecks[0].Type = libsveltosv1beta1.LivenessTypeAddons
		addonsOnly.Spec.LivenessChecks[0].LivenessSourceRef = nil

		clusterHealthChecks := &libsveltosv1beta1.ClusterHealthCheckList{
			Items: []libsveltosv1beta1.ClusterHealthCheck{*active, *deleting, *addonsOnly},
		}
		wanted := controllers.BuildWantedReports(clusterHealthChecks, []corev1.ObjectReference{shardCluster})

		Expect(wanted).To(HaveLen(1))
		Expect(controllers.IsReportWanted(wanted, &shardCluster, getHealthCheckReportForName(healthCheckName))).To(BeTrue())
		Expect(controllers.IsReportWanted(wanted, &otherShardCluster, getHealthCheckReportForName(healthCheckName))).To(BeFalse())
		Expect(controllers.IsReportWanted(wanted, &shardCluster, getHealthCheckReportForName(randomString()))).To(BeFalse())
	})

	It("isWanted leaves reports not coming from a managed cluster to updateHealthCheckReport", func() {
		cluster := corev1.ObjectReference{Namespace: randomString(), Name: randomString(),
			Kind: ClusterKind, APIVersion: clusterv1.GroupVersion.String()}
		wanted := controllers.BuildWantedReports(&libsveltosv1beta1.ClusterHealthCheckList{}, nil)

		By("Report already in the management cluster (ClusterName set)")
		report := getHealthCheckReportForName(randomString())
		report.Spec.ClusterName = cluster.Name
		Expect(controllers.IsReportWanted(wanted, &cluster, report)).To(BeTrue())

		By("Report without the HealthCheck label")
		report = getHealthCheckReportForName(randomString())
		report.Labels = nil
		Expect(controllers.IsReportWanted(wanted, &cluster, report)).To(BeTrue())
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
				cmVersionKey: version,
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

	It("collectAndProcessAllHealthCheckReports processes the HealthCheckReports of a cluster in pull mode "+
		"without an agent version ConfigMap", func() {
		// The agent of a cluster in pull mode runs in the managed cluster: no agent version ConfigMap
		// is created in the management cluster for it, so that check must not be done.
		sveltosCluster := &libsveltosv1beta1.SveltosCluster{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: randomString(),
				Name:      randomString(),
			},
			Spec: libsveltosv1beta1.SveltosClusterSpec{
				PullMode: true,
			},
		}

		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: sveltosCluster.Namespace}}
		Expect(testEnv.Create(context.TODO(), ns)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, ns)).To(Succeed())

		Expect(testEnv.Create(context.TODO(), sveltosCluster)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, sveltosCluster)).To(Succeed())

		sveltosCluster.Status.Ready = true
		Expect(testEnv.Status().Update(context.TODO(), sveltosCluster)).To(Succeed())

		healthCheckName := randomString()
		clusterType := libsveltosv1beta1.ClusterTypeSveltos
		healthCheckReport := &libsveltosv1beta1.HealthCheckReport{
			ObjectMeta: metav1.ObjectMeta{
				Name:      randomString(),
				Namespace: sveltosCluster.Namespace,
				Labels: map[string]string{
					libsveltosv1beta1.HealthCheckNameLabel:              healthCheckName,
					libsveltosv1beta1.HealthCheckReportClusterNameLabel: sveltosCluster.Name,
					libsveltosv1beta1.HealthCheckReportClusterTypeLabel: strings.ToLower(string(clusterType)),
				},
			},
			Spec: libsveltosv1beta1.HealthCheckReportSpec{
				HealthCheckName:  healthCheckName,
				ClusterNamespace: sveltosCluster.Namespace,
				ClusterName:      sveltosCluster.Name,
				ClusterType:      clusterType,
			},
		}
		Expect(testEnv.Create(context.TODO(), healthCheckReport)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, healthCheckReport)).To(Succeed())

		sveltosClusterRef := corev1.ObjectReference{
			Namespace:  sveltosCluster.Namespace,
			Name:       sveltosCluster.Name,
			Kind:       libsveltosv1beta1.SveltosClusterKind,
			APIVersion: libsveltosv1beta1.GroupVersion.String(),
		}

		logger := textlogger.NewLogger(textlogger.NewConfig())
		Expect(controllers.CollectAndProcessAllHealthCheckReports(context.TODO(), testEnv.Client,
			[]corev1.ObjectReference{sveltosClusterRef}, version, logger)).To(Succeed())

		Eventually(func() bool {
			current := &libsveltosv1beta1.HealthCheckReport{}
			err := testEnv.Get(context.TODO(),
				types.NamespacedName{Namespace: healthCheckReport.Namespace, Name: healthCheckReport.Name}, current)
			return err == nil && current.Status.Phase != nil &&
				*current.Status.Phase == libsveltosv1beta1.ReportProcessed
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
		if currentHealthCheckReport.Spec.HealthCheckName != healthCheckName {
			By("Spec HealthCheckName not set")
			return false
		}
		v, ok := currentHealthCheckReport.Labels[libsveltosv1beta1.HealthCheckNameLabel]
		return ok && v == healthCheckName
	}, timeout, pollingInterval).Should(BeTrue())
}

// getClusterHealthCheckForHealthCheck returns a ClusterHealthCheck matching the clusters, with a single
// HealthCheck liveness check referencing healthCheckName
func getClusterHealthCheckForHealthCheck(healthCheckName string,
	clusters ...corev1.ObjectReference) *libsveltosv1beta1.ClusterHealthCheck {

	chc := &libsveltosv1beta1.ClusterHealthCheck{
		ObjectMeta: metav1.ObjectMeta{Name: randomString()},
		Spec: libsveltosv1beta1.ClusterHealthCheckSpec{
			LivenessChecks: []libsveltosv1beta1.LivenessCheck{
				{
					Name: randomString(),
					Type: libsveltosv1beta1.LivenessTypeHealthCheck,
					LivenessSourceRef: &corev1.ObjectReference{
						APIVersion: libsveltosv1beta1.GroupVersion.String(),
						Kind:       libsveltosv1beta1.HealthCheckKind,
						Name:       healthCheckName,
					},
				},
			},
		},
	}
	chc.Status.MatchingClusterRefs = clusters
	return chc
}

// getHealthCheckReportForName returns a HealthCheckReport as collected from a managed cluster
func getHealthCheckReportForName(healthCheckName string) *libsveltosv1beta1.HealthCheckReport {
	return &libsveltosv1beta1.HealthCheckReport{
		ObjectMeta: metav1.ObjectMeta{
			Name:   healthCheckName,
			Labels: map[string]string{libsveltosv1beta1.HealthCheckNameLabel: healthCheckName},
		},
	}
}
