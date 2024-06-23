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

package fv_test

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
)

// This test verifies that a ClusterHealthCheck with:
// - liveness check of type add-ons
// - notifications of type Kubernetes events
// when add-ons are deployed, event is generated
var _ = Describe("Liveness: add-ons Notifications: events", func() {
	const (
		namePrefix = "addons-events-"
	)

	It("Verifies events are delivered", Label("FV"), func() {
		lc := libsveltosv1beta1.LivenessCheck{Name: randomString(), Type: libsveltosv1beta1.LivenessTypeAddons}

		notification := libsveltosv1beta1.Notification{Name: randomString(), Type: libsveltosv1beta1.NotificationTypeKubernetesEvent}

		Byf("Create a ClusterHealthCheck matching Cluster %s/%s", kindWorkloadCluster.Namespace, kindWorkloadCluster.Name)
		clusterHealthCheck := getClusterHealthCheck(namePrefix, map[string]string{key: value},
			[]libsveltosv1beta1.LivenessCheck{lc}, []libsveltosv1beta1.Notification{notification})
		Expect(k8sClient.Create(context.TODO(), clusterHealthCheck)).To(Succeed())

		Byf("Create a ClusterProfile matching Cluster %s/%s", kindWorkloadCluster.Namespace, kindWorkloadCluster.Name)
		clusterProfile := getClusterProfile(namePrefix, map[string]string{key: value})
		clusterProfile.Spec.SyncMode = configv1beta1.SyncModeContinuous
		Expect(k8sClient.Create(context.TODO(), clusterProfile)).To(Succeed())

		verifyClusterProfileMatches(clusterProfile)

		clusterSummary := verifyClusterSummary(clusterProfile, kindWorkloadCluster.Namespace, kindWorkloadCluster.Name)

		Byf("Verifying ClusterSummary %s status is set to Deployed for Helm feature", clusterSummary.Name)
		verifyFeatureStatusIsProvisioned(kindWorkloadCluster.Namespace, clusterSummary.Name, configv1beta1.FeatureHelm)

		Byf("Verifying ClusterHealthCheck %s is set to Provisioned", clusterHealthCheck.Name)
		verifyClusterHealthCheckStatus(clusterHealthCheck.Name, kindWorkloadCluster.Namespace, kindWorkloadCluster.Name)

		Byf("Deleting ClusterHealthCheck")
		deleteClusterHealthCheck(clusterHealthCheck.Name)

		Byf("Deleting ClusterProfile")
		deleteClusterProfile(clusterProfile)
	})
})
