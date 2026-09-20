/*
Copyright 2026. Sveltos SRL. All rights reserved.

This file is part of Sveltos Enterprise. See the LICENSE file at the root
of this repository.
*/

package fv_test

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
)

const namespaceKind = "Namespace"

// namespaceAlwaysHealthyLuaScript always reports every matched resource as Healthy: Namespace
// is cluster-scoped and always has at least one instance (kube-system, default, ...), so this
// is a guaranteed non-empty result as long as evaluate does not error out.
const namespaceAlwaysHealthyLuaScript = `
function evaluate()
   local statuses = {}
   for _, resource in ipairs(resources) do
     table.insert(statuses, {resource=resource, status="Healthy", message=""})
   end
   local hs = {}
   hs.resources = statuses
   return hs
end`

var _ = Describe("HealthCheckReport: sveltos-agent evaluation failure is surfaced", func() {
	const namePrefix = "agent-failure-"

	It("AgentFailureMessage is set when sveltos-agent's evaluation fails, and cleared once fixed",
		Label("FV", "PULLMODE"), func() {

			healthCheck := &libsveltosv1beta1.HealthCheck{
				ObjectMeta: metav1.ObjectMeta{
					Name: namePrefix + randomString(),
				},
				Spec: libsveltosv1beta1.HealthCheckSpec{
					ResourceSelectors: []libsveltosv1beta1.ResourceSelector{
						{
							Group:   "",
							Version: apiVersionV1,
							Kind:    namespaceKind,
						},
					},
					EvaluateHealth: namespaceAlwaysHealthyLuaScript,
				},
			}
			Byf("Creating HealthCheck %s", healthCheck.Name)
			Expect(k8sClient.Create(context.TODO(), healthCheck)).To(Succeed())

			lc := libsveltosv1beta1.LivenessCheck{
				Name: randomString(),
				Type: libsveltosv1beta1.LivenessTypeHealthCheck,
				LivenessSourceRef: &corev1.ObjectReference{
					Name:       healthCheck.Name,
					APIVersion: libsveltosv1beta1.GroupVersion.String(),
					Kind:       libsveltosv1beta1.HealthCheckKind,
				},
			}

			Byf("Create a ClusterHealthCheck matching Cluster %s/%s",
				kindWorkloadCluster.GetNamespace(), kindWorkloadCluster.GetName())
			clusterHealthCheck := getClusterHealthCheck(namePrefix, map[string]string{key: value},
				[]libsveltosv1beta1.LivenessCheck{lc}, []libsveltosv1beta1.Notification{})
			Expect(k8sClient.Create(context.TODO(), clusterHealthCheck)).To(Succeed())

			Byf("Verifying HealthCheckReport for HealthCheck %s is present in the management cluster, "+
				"with no AgentFailureMessage", healthCheck.Name)
			verifyAgentFailureMessage(healthCheck.Name, false)

			Byf("Introducing a broken Lua evaluate script, so sveltos-agent's evaluation errors out")
			currentHealthCheck := &libsveltosv1beta1.HealthCheck{}
			Expect(k8sClient.Get(context.TODO(), types.NamespacedName{Name: healthCheck.Name},
				currentHealthCheck)).To(Succeed())
			currentHealthCheck.Spec.EvaluateHealth = "this is not valid lua {{{"
			Expect(k8sClient.Update(context.TODO(), currentHealthCheck)).To(Succeed())

			Byf("Verifying HealthCheckReport AgentFailureMessage gets set")
			verifyAgentFailureMessage(healthCheck.Name, true)

			Byf("Fixing the Lua evaluate script")
			Expect(k8sClient.Get(context.TODO(), types.NamespacedName{Name: healthCheck.Name},
				currentHealthCheck)).To(Succeed())
			currentHealthCheck.Spec.EvaluateHealth = namespaceAlwaysHealthyLuaScript
			Expect(k8sClient.Update(context.TODO(), currentHealthCheck)).To(Succeed())

			Byf("Verifying HealthCheckReport AgentFailureMessage is cleared once evaluation succeeds again")
			verifyAgentFailureMessage(healthCheck.Name, false)

			Byf("Deleting ClusterHealthCheck")
			deleteClusterHealthCheck(clusterHealthCheck.Name)

			Byf("Deleting HealthCheck %s", healthCheck.Name)
			Expect(k8sClient.Get(context.TODO(), types.NamespacedName{Name: healthCheck.Name},
				currentHealthCheck)).To(Succeed())
			Expect(k8sClient.Delete(context.TODO(), currentHealthCheck)).To(Succeed())
		})
})

// verifyAgentFailureMessage waits for the management cluster's copy of HealthCheckReport.Status.
// AgentFailureMessage to be set (or cleared, if wantSet is false).
func verifyAgentFailureMessage(healthCheckName string, wantSet bool) {
	Byf("Verifying HealthCheckReport for HealthCheck %s AgentFailureMessage is set: %t", healthCheckName, wantSet)
	clusterType := libsveltosv1beta1.ClusterTypeCapi
	if kindWorkloadCluster.GetKind() == libsveltosv1beta1.SveltosClusterKind {
		clusterType = libsveltosv1beta1.ClusterTypeSveltos
	}
	labels := libsveltosv1beta1.GetHealthCheckReportLabels(healthCheckName, kindWorkloadCluster.GetName(), &clusterType)
	listOptions := []client.ListOption{
		client.InNamespace(kindWorkloadCluster.GetNamespace()),
		client.MatchingLabels(labels),
	}
	Eventually(func() bool {
		healthCheckReportList := &libsveltosv1beta1.HealthCheckReportList{}
		if err := k8sClient.List(context.TODO(), healthCheckReportList, listOptions...); err != nil {
			return false
		}
		if len(healthCheckReportList.Items) != 1 {
			return false
		}
		return (healthCheckReportList.Items[0].Status.AgentFailureMessage != nil) == wantSet
	}, timeout, pollingInterval).Should(BeTrue())
}
