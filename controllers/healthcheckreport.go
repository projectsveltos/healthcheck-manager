/*
Copyright 2025. projectsveltos.io. All rights reserved.

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

package controllers

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
	"github.com/projectsveltos/libsveltos/lib/clusterproxy"
	logs "github.com/projectsveltos/libsveltos/lib/logsettings"
	"github.com/projectsveltos/libsveltos/lib/mgmtagent"
	libsveltosset "github.com/projectsveltos/libsveltos/lib/set"
)

// HealthCheckReports reside in the same cluster as the sveltos-agent component.
// This function dynamically selects the appropriate Kubernetes client:
// - Management cluster's client if sveltos-agent is deployed there.
// - A managed cluster's client (obtained via clusterproxy) if sveltos-agent is in a managed cluster.
func getHealthCheckReportClient(ctx context.Context, clusterNamespace, clusterName string, clusterType libsveltosv1beta1.ClusterType,
	logger logr.Logger) (client.Client, error) {

	if getAgentInMgmtCluster() {
		return getManagementClusterClient(), nil
	}

	isPullMode, err := clusterproxy.IsClusterInPullMode(ctx, getManagementClusterClient(), clusterNamespace, clusterName,
		clusterType, logger)
	if err != nil {
		return nil, err
	}

	if isPullMode {
		// In pull mode the applier copies the EventReports to the management cluster.
		return getManagementClusterClient(), nil
	}

	// Sveltos resources are always created using cluster-admin so that admin does not need to be
	// given such permissions.
	return clusterproxy.GetKubernetesClient(ctx, getManagementClusterClient(),
		clusterNamespace, clusterName, "", "", clusterType, logger)
}

// When sveltos-agent is running in the management cluster, HealthChecks are not copied to the managed cluster.
// Rather a ConfigMap is used to tell sveltos-agent for a given cluster, which HealthChecks it should process.
// The management cluster contains all HealthChecks but only a subset needs to be evaluated for a specific cluster.
func addHealthCheckToConfigMap(ctx context.Context, c client.Client, clusterNamespace, clusterName string,
	clusterType libsveltosv1beta1.ClusterType, et *libsveltosv1beta1.ClusterHealthCheck, es *libsveltosv1beta1.HealthCheck,
	logger logr.Logger) error {

	configMapNamespace := clusterNamespace
	configMapName := mgmtagent.GetConfigMapName(clusterName, clusterType)
	eventSourceEntryKey := mgmtagent.GetKeyForHealthCheck(et.Name, es.Name)

	currentConfigMap := &corev1.ConfigMap{}
	err := c.Get(ctx, types.NamespacedName{Namespace: configMapNamespace, Name: configMapName}, currentConfigMap)
	if err != nil {
		if apierrors.IsNotFound(err) {
			currentConfigMap = &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: configMapNamespace,
					Name:      configMapName,
				},
				Data: map[string]string{
					eventSourceEntryKey: es.Name,
				},
			}
			logger.V(logs.LogDebug).Info(fmt.Sprintf("creating entry %s in ConfigMap %s/%s",
				eventSourceEntryKey, configMapNamespace, configMapName))
			return c.Create(ctx, currentConfigMap)
		}
		return err
	}

	if currentConfigMap.Data == nil {
		currentConfigMap.Data = map[string]string{}
	}
	currentConfigMap.Data[eventSourceEntryKey] = es.Name
	logger.V(logs.LogDebug).Info(fmt.Sprintf("creating entry %s in ConfigMap %s/%s",
		eventSourceEntryKey, configMapNamespace, configMapName))
	return c.Update(ctx, currentConfigMap)
}

// When sveltos-agent is running in the management cluster, HealthChecks are not copied to the managed cluster.
// Rather a ConfigMap is used to tell sveltos-agent for a given cluster, which HealthChecks it should process.
// The management cluster contains all HealthChecks but only a subset needs to be evaluated for a specific cluster.
func removeHealthCheckFromConfigMap(ctx context.Context, c client.Client, clusterNamespace, clusterName string,
	clusterType libsveltosv1beta1.ClusterType, et *libsveltosv1beta1.ClusterHealthCheck, leaveEntries *libsveltosset.Set,
	logger logr.Logger) error {

	configMapNamespace := clusterNamespace
	configMapName := mgmtagent.GetConfigMapName(clusterName, clusterType)

	currentConfigMap := &corev1.ConfigMap{}
	err := c.Get(ctx, types.NamespacedName{Namespace: configMapNamespace, Name: configMapName}, currentConfigMap)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}

		return err
	}

	logger.V(logs.LogDebug).Info(fmt.Sprintf("removing entries for eventTrigger %s in ConfigMap %s/%s except %v",
		et.Name, configMapNamespace, configMapName, leaveEntries.Items()))

	for k, v := range currentConfigMap.Data {
		ref := &corev1.ObjectReference{
			APIVersion: libsveltosv1beta1.GroupVersion.String(),
			Kind:       libsveltosv1beta1.HealthCheckKind,
			Name:       v,
		}

		if leaveEntries.Has(ref) {
			continue
		}
		if mgmtagent.IsHealthCheckEntryForClusterHealthCheck(k, et.Name) {
			delete(currentConfigMap.Data, k)
		}
	}

	return c.Update(ctx, currentConfigMap)
}
