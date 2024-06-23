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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2/textlogger"

	"github.com/projectsveltos/healthcheck-manager/controllers"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
)

var _ = Describe("ReloaderReport Collection", func() {

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
			getClusterRef(cluster), textlogger.NewLogger(textlogger.NewConfig(textlogger.Verbosity(1))))).To(Succeed())

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
			getClusterRef(cluster), textlogger.NewLogger(textlogger.NewConfig(textlogger.Verbosity(1))))).To(Succeed())

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
		if len(reloaderReports.Items) != 1 {
			By(fmt.Sprintf("found %d reloaderReports", len(reloaderReports.Items)))
			return false
		}
		if !reflect.DeepEqual(reloaderReports.Items[0].Spec.ResourcesToReload, expectedReloaderInfo) {
			By("ReloaderInfo does not match")
			return false
		}
		return true
	}, timeout, pollingInterval).Should(BeTrue())
}
