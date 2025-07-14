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
	"fmt"
	"reflect"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/cluster-api/util"
	"sigs.k8s.io/controller-runtime/pkg/client"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
)

const (
	key   = "env"
	value = "fv"
)

var (
	evaluateFunction = `
   function evaluate()
     statuses = {}
     status = "Progressing"
     message = ""
	 for _, resource in ipairs(resources) do
       if resource.status ~= nil then
         if resource.status.availableReplicas ~= nil then
           if resource.status.availableReplicas == resource.spec.replicas then
             status = "Healthy"
           end
           if resource.status.availableReplicas ~= resource.spec.replicas then
             status = "Progressing"
             message = "expected replicas: " .. resource.spec.replicas .. " available: " .. resource.status.availableReplicas
           end
         end
       end
	   table.insert(statuses, {resource=resource, status=status, message=message})
	 end
	 local hs = {}
	 if #statuses > 0 then
	   hs.resources = statuses
	 end
	 return hs
   end`
)

// Byf is a simple wrapper around By.
func Byf(format string, a ...interface{}) {
	By(fmt.Sprintf(format, a...)) // ignore_by_check
}

func randomString() string {
	const length = 10
	return util.RandomString(length)
}

func getClusterSummary(ctx context.Context,
	clusterProfileName, clusterNamespace, clusterName string) (*configv1beta1.ClusterSummary, error) {

	clusterType := libsveltosv1beta1.ClusterTypeCapi
	if kindWorkloadCluster.GetKind() == libsveltosv1beta1.SveltosClusterKind {
		clusterType = libsveltosv1beta1.ClusterTypeSveltos
	}

	listOptions := []client.ListOption{
		client.InNamespace(clusterNamespace),
		client.MatchingLabels{
			"projectsveltos.io/cluster-profile-name": clusterProfileName,
			configv1beta1.ClusterNameLabel:           clusterName,
			configv1beta1.ClusterTypeLabel:           string(clusterType),
		},
	}

	clusterSummaryList := &configv1beta1.ClusterSummaryList{}
	if err := k8sClient.List(ctx, clusterSummaryList, listOptions...); err != nil {
		return nil, err
	}

	if len(clusterSummaryList.Items) == 0 {
		return nil, apierrors.NewNotFound(
			schema.GroupResource{Group: configv1beta1.GroupVersion.Group, Resource: configv1beta1.ClusterSummaryKind}, "")
	}

	if len(clusterSummaryList.Items) != 1 {
		return nil, fmt.Errorf("more than one clustersummary found for cluster %s/%s created by %s",
			clusterNamespace, clusterName, clusterProfileName)
	}

	return &clusterSummaryList.Items[0], nil
}

func verifyClusterSummary(clusterProfile *configv1beta1.ClusterProfile,
	clusterNamespace, clusterName string) *configv1beta1.ClusterSummary {

	Byf("Verifying ClusterSummary is created")
	Eventually(func() bool {
		clusterSummary, err := getClusterSummary(context.TODO(),
			clusterProfile.Name, clusterNamespace, clusterName)
		return err == nil &&
			clusterSummary != nil
	}, timeout, pollingInterval).Should(BeTrue())

	clusterSummary, err := getClusterSummary(context.TODO(),
		clusterProfile.Name, clusterNamespace, clusterName)
	Expect(err).To(BeNil())
	Expect(clusterSummary).ToNot(BeNil())

	Byf("Verifying ClusterSummary ownerReference")
	ref, err := getClusterSummaryOwnerReference(clusterSummary)
	Expect(err).To(BeNil())
	Expect(ref).ToNot(BeNil())
	Expect(ref.Name).To(Equal(clusterProfile.Name))

	Byf("Verifying ClusterSummary configuration")
	Eventually(func() error {
		var currentClusterSummary *configv1beta1.ClusterSummary
		currentClusterSummary, err = getClusterSummary(context.TODO(),
			clusterProfile.Name, clusterNamespace, clusterName)
		if err != nil {
			return err
		}

		if !reflect.DeepEqual(currentClusterSummary.Spec.ClusterProfileSpec.HelmCharts,
			clusterProfile.Spec.HelmCharts) {

			return fmt.Errorf("helmCharts do not match")
		}
		if !reflect.DeepEqual(currentClusterSummary.Spec.ClusterProfileSpec.PolicyRefs,
			clusterProfile.Spec.PolicyRefs) {

			return fmt.Errorf("policyRefs do not match")
		}
		if !reflect.DeepEqual(currentClusterSummary.Spec.ClusterNamespace, clusterNamespace) {
			return fmt.Errorf("clusterNamespace does not match")
		}
		if !reflect.DeepEqual(currentClusterSummary.Spec.ClusterName, clusterName) {
			return fmt.Errorf("clusterName does not match")
		}
		return nil
	}, timeout, pollingInterval).Should(BeNil())

	clusterSummary, err = getClusterSummary(context.TODO(),
		clusterProfile.Name, clusterNamespace, clusterName)
	Expect(err).To(BeNil())
	Expect(clusterSummary).ToNot(BeNil())

	return clusterSummary
}

func verifyFeatureStatusIsProvisioned(clusterSummaryNamespace, clusterSummaryName string, featureID libsveltosv1beta1.FeatureID) {
	Eventually(func() bool {
		currentClusterSummary := &configv1beta1.ClusterSummary{}
		err := k8sClient.Get(context.TODO(),
			types.NamespacedName{Namespace: clusterSummaryNamespace, Name: clusterSummaryName},
			currentClusterSummary)
		if err != nil {
			return false
		}
		for i := range currentClusterSummary.Status.FeatureSummaries {
			if currentClusterSummary.Status.FeatureSummaries[i].FeatureID == featureID &&
				currentClusterSummary.Status.FeatureSummaries[i].Status == libsveltosv1beta1.FeatureStatusProvisioned {

				return true
			}
		}
		return false
	}, timeout, pollingInterval).Should(BeTrue())
}

func getClusterSummaryOwnerReference(clusterSummary *configv1beta1.ClusterSummary) (*configv1beta1.ClusterProfile, error) {
	Byf("Checking clusterSummary %s owner reference is set", clusterSummary.Name)
	for _, ref := range clusterSummary.OwnerReferences {
		if ref.Kind != configv1beta1.ClusterProfileKind {
			continue
		}
		gv, err := schema.ParseGroupVersion(ref.APIVersion)
		if err != nil {
			return nil, errors.WithStack(err)
		}
		if gv.Group == configv1beta1.GroupVersion.Group {
			clusterProfile := &configv1beta1.ClusterProfile{}
			err := k8sClient.Get(context.TODO(), types.NamespacedName{Name: ref.Name}, clusterProfile)
			return clusterProfile, err
		}
	}
	return nil, nil
}

func verifyClusterProfileMatches(clusterProfile *configv1beta1.ClusterProfile) {
	Byf("Verifying Cluster %s/%s is a match for ClusterProfile %s",
		kindWorkloadCluster.GetNamespace(), kindWorkloadCluster.GetName(), clusterProfile.Name)

	Eventually(func() bool {
		currentClusterProfile := &configv1beta1.ClusterProfile{}
		err := k8sClient.Get(context.TODO(), types.NamespacedName{Name: clusterProfile.Name}, currentClusterProfile)
		return err == nil &&
			len(currentClusterProfile.Status.MatchingClusterRefs) == 1 &&
			currentClusterProfile.Status.MatchingClusterRefs[0].Namespace == kindWorkloadCluster.GetNamespace() &&
			currentClusterProfile.Status.MatchingClusterRefs[0].Name == kindWorkloadCluster.GetName() &&
			currentClusterProfile.Status.MatchingClusterRefs[0].APIVersion == kindWorkloadCluster.GetAPIVersion()
	}, timeout, pollingInterval).Should(BeTrue())
}

func verifyClusterHealthCheckStatus(clusterHealthCheckName, clusterNamespace, clusterName string) {
	Eventually(func() bool {
		currentClusterHealthCheck := &libsveltosv1beta1.ClusterHealthCheck{}
		err := k8sClient.Get(context.TODO(),
			types.NamespacedName{Name: clusterHealthCheckName},
			currentClusterHealthCheck)
		if err != nil {
			return false
		}

		clusterMatching := false
		for i := range currentClusterHealthCheck.Status.MatchingClusterRefs {
			mCluster := &currentClusterHealthCheck.Status.MatchingClusterRefs[i]
			if mCluster.Namespace == clusterNamespace && mCluster.Name == clusterName {
				clusterMatching = true
			}
		}
		if !clusterMatching {
			return false
		}

		clusterConditionFound := false
		for i := range currentClusterHealthCheck.Status.ClusterConditions {
			cc := &currentClusterHealthCheck.Status.ClusterConditions[i]
			if isClusterConditionForCluster(cc, clusterNamespace, clusterName) {
				clusterConditionFound = true

				if !verifyLivenessChecks(cc) {
					return false
				}

				if !verifyNotifications(cc) {
					return false
				}
			}
		}

		return clusterConditionFound
	}, timeout, pollingInterval).Should(BeTrue())
}

func verifyLivenessChecks(cc *libsveltosv1beta1.ClusterCondition) bool {
	if len(cc.Conditions) == 0 {
		return false
	}

	for i := range cc.Conditions {
		c := &cc.Conditions[i]
		if c.Status != corev1.ConditionTrue {
			return false
		}
	}

	return true
}

func verifyNotifications(cc *libsveltosv1beta1.ClusterCondition) bool {
	if len(cc.NotificationSummaries) == 0 {
		return false
	}

	for i := range cc.NotificationSummaries {
		ns := &cc.NotificationSummaries[i]
		if ns.Status != libsveltosv1beta1.NotificationStatusDelivered {
			return false
		}
	}

	return true
}

func getClusterHealthCheck(namePrefix string, clusterLabels map[string]string,
	lc []libsveltosv1beta1.LivenessCheck, notifications []libsveltosv1beta1.Notification,
) *libsveltosv1beta1.ClusterHealthCheck {

	clusterHealthCheck := &libsveltosv1beta1.ClusterHealthCheck{
		ObjectMeta: metav1.ObjectMeta{
			Name: namePrefix + randomString(),
			Annotations: map[string]string{
				randomString(): randomString(),
			},
		},
		Spec: libsveltosv1beta1.ClusterHealthCheckSpec{
			ClusterSelector: libsveltosv1beta1.Selector{
				LabelSelector: metav1.LabelSelector{
					MatchLabels: clusterLabels,
				},
			},
			LivenessChecks: lc,
			Notifications:  notifications,
		},
	}

	return clusterHealthCheck
}

// getClusterProfile returns a ClusterProfile with Kyverno helm chart
func getClusterProfile(namePrefix string, clusterLabels map[string]string) *configv1beta1.ClusterProfile {
	clusterProfile := &configv1beta1.ClusterProfile{
		ObjectMeta: metav1.ObjectMeta{
			Name: namePrefix + randomString(),
		},
		Spec: configv1beta1.Spec{
			ClusterSelector: libsveltosv1beta1.Selector{
				LabelSelector: metav1.LabelSelector{
					MatchLabels: clusterLabels,
				},
			},
			HelmCharts: []configv1beta1.HelmChart{
				{
					RepositoryURL:    "https://kyverno.github.io/kyverno/",
					RepositoryName:   "kyverno",
					ChartName:        "kyverno/kyverno",
					ChartVersion:     "v2.6.5",
					ReleaseName:      "kyverno-latest",
					ReleaseNamespace: "kyverno",
					HelmChartAction:  configv1beta1.HelmChartActionInstall,
				},
			},
		},
	}

	return clusterProfile
}

// isClusterConditionForCluster returns true if the ClusterCondition is for the cluster clusterType, clusterNamespace,
// clusterName
func isClusterConditionForCluster(cc *libsveltosv1beta1.ClusterCondition, clusterNamespace, clusterName string) bool {
	return cc.ClusterInfo.Cluster.Namespace == clusterNamespace &&
		cc.ClusterInfo.Cluster.Name == clusterName
}

func deleteClusterHealthCheck(clusterHealthCheckName string) {
	currentClusterHealthCheck := &libsveltosv1beta1.ClusterHealthCheck{}
	err := k8sClient.Get(context.TODO(),
		types.NamespacedName{Name: clusterHealthCheckName},
		currentClusterHealthCheck)
	Expect(err).To(BeNil())

	Expect(k8sClient.Delete(context.TODO(), currentClusterHealthCheck)).To(Succeed())

	Byf("Verifying ClusterHealthCheck %s is gone", clusterHealthCheckName)

	Eventually(func() bool {
		err = k8sClient.Get(context.TODO(),
			types.NamespacedName{Name: clusterHealthCheckName},
			currentClusterHealthCheck)
		return apierrors.IsNotFound(err)
	}, timeout, pollingInterval).Should(BeTrue())
}

// deleteClusterProfile deletes ClusterProfile and verifies all ClusterSummaries created by this ClusterProfile
// instances are also gone
func deleteClusterProfile(clusterProfile *configv1beta1.ClusterProfile) {
	listOptions := []client.ListOption{
		client.MatchingLabels{
			"projectsveltos.io/cluster-profile-name": clusterProfile.Name,
		},
	}
	clusterSummaryList := &configv1beta1.ClusterSummaryList{}
	Expect(k8sClient.List(context.TODO(), clusterSummaryList, listOptions...)).To(Succeed())

	Byf("Deleting the ClusterProfile %s", clusterProfile.Name)
	currentClusterProfile := &configv1beta1.ClusterProfile{}
	Expect(k8sClient.Get(context.TODO(), types.NamespacedName{Name: clusterProfile.Name}, currentClusterProfile)).To(BeNil())
	Expect(k8sClient.Delete(context.TODO(), currentClusterProfile)).To(Succeed())

	for i := range clusterSummaryList.Items {
		Byf("Verifying ClusterSummary %s are gone", clusterSummaryList.Items[i].Name)
	}
	Eventually(func() bool {
		for i := range clusterSummaryList.Items {
			clusterSummaryNamespace := clusterSummaryList.Items[i].Namespace
			clusterSummaryName := clusterSummaryList.Items[i].Name
			currentClusterSummary := &configv1beta1.ClusterSummary{}
			err := k8sClient.Get(context.TODO(),
				types.NamespacedName{Namespace: clusterSummaryNamespace, Name: clusterSummaryName}, currentClusterSummary)
			if err == nil || !apierrors.IsNotFound(err) {
				return false
			}
		}
		return true
	}, timeout, pollingInterval).Should(BeTrue())

	Byf("Verifying ClusterProfile %s is gone", clusterProfile.Name)

	Eventually(func() bool {
		err := k8sClient.Get(context.TODO(), types.NamespacedName{Name: clusterProfile.Name}, currentClusterProfile)
		return apierrors.IsNotFound(err)
	}, timeout, pollingInterval).Should(BeTrue())
}

// getKindWorkloadClusterKubeconfig returns client to access the kind cluster used as workload cluster
func getKindWorkloadClusterKubeconfig() (client.Client, error) {
	kubeconfigPath := "workload_kubeconfig" // this file is created in this directory by Makefile during cluster creation
	config, err := clientcmd.LoadFromFile(kubeconfigPath)
	if err != nil {
		return nil, err
	}
	restConfig, err := clientcmd.NewDefaultClientConfig(*config, &clientcmd.ConfigOverrides{}).ClientConfig()
	if err != nil {
		return nil, err
	}
	return client.New(restConfig, client.Options{Scheme: scheme})
}
