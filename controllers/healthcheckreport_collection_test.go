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

	"github.com/go-logr/logr"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2/textlogger"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/projectsveltos/healthcheck-manager/controllers"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
)

var _ = Describe("HealthCheck Deployer", func() {
	var healthCheck *libsveltosv1beta1.HealthCheck
	var logger logr.Logger

	BeforeEach(func() {
		healthCheck = getHealthCheckInstance(randomString())
		logger = textlogger.NewLogger(textlogger.NewConfig(textlogger.Verbosity(1)))
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

		Expect(controllers.RemoveHealthCheckReports(context.TODO(), c, healthCheck, logger)).To(Succeed())

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
