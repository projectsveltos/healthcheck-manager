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

package controllers

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	libsveltosv1alpha1 "github.com/projectsveltos/libsveltos/api/v1alpha1"
	logs "github.com/projectsveltos/libsveltos/lib/logsettings"
)

// sendNotification delivers notification
func sendNotification(ctx context.Context, c client.Client, clusterNamespace, clusterName string,
	clusterType libsveltosv1alpha1.ClusterType, chc *libsveltosv1alpha1.ClusterHealthCheck,
	n *libsveltosv1alpha1.Notification, conditions []libsveltosv1alpha1.Condition, logger logr.Logger) error {

	logger = logger.WithValues("notification", fmt.Sprintf("%s:%s", n.Type, n.Name))
	logger.V(logs.LogDebug).Info("deliver notification")

	var err error
	switch n.Type {
	case libsveltosv1alpha1.NotificationTypeKubernetesEvent:
		err = sendKubernetesNotification(ctx, c, clusterNamespace, clusterName, clusterType, chc, n, conditions, logger)
	default:
		logger.V(logs.LogInfo).Info("no handler registered for notification")
		panic(1)
	}

	if err != nil {
		logger.V(logs.LogInfo).Info("failed to send notification")
		return err
	}
	logger.V(logs.LogDebug).Info("notification delivered")
	return nil
}

func sendKubernetesNotification(ctx context.Context, c client.Client, clusterNamespace, clusterName string,
	clusterType libsveltosv1alpha1.ClusterType, chc *libsveltosv1alpha1.ClusterHealthCheck,
	n *libsveltosv1alpha1.Notification, conditions []libsveltosv1alpha1.Condition, logger logr.Logger) error {

	passing := true
	message := fmt.Sprintf("cluster %s:%s/%s\n", clusterType, clusterNamespace, clusterName)
	for i := range conditions {
		c := &conditions[i]
		if c.Status != corev1.ConditionTrue {
			passing = false
			message += fmt.Sprintf("liveness check %s failing %q\n", c.Type, c.Message)
		}
	}

	if passing {
		message += "all add-ons are deployed"
		logger.V(logs.LogDebug).Info("add-ons deployed")
	} else {
		logger.V(logs.LogDebug).Info("all or some add-ons are not deployed")
	}

	r := getManagementRecorder()
	r.Eventf(chc, corev1.EventTypeNormal,
		"ClusterHealthCheck", message)

	r.Event(chc, corev1.EventTypeNormal,
		"ClusterHealthCheck", message)

	return nil
}

// buildNotificationStatusMap creates a map reporting notification status by walking over ClusterHealthCheck status
func buildNotificationStatusMap(clusterNamespace, clusterName string,
	clusterType libsveltosv1alpha1.ClusterType, chc *libsveltosv1alpha1.ClusterHealthCheck) map[string]libsveltosv1alpha1.NotificationStatus {

	notificationStatus := make(map[string]libsveltosv1alpha1.NotificationStatus)

	for i := range chc.Status.ClusterConditions {
		cc := &chc.Status.ClusterConditions[i]
		if isClusterConditionForCluster(cc, clusterNamespace, clusterName, clusterType) {
			for j := range cc.NotificationSummaries {
				n := &cc.NotificationSummaries[j]
				notificationStatus[n.Name] = n.Status
			}
		}
	}

	return notificationStatus
}

// doSendNotification returns true if notification needs to be delivered, which happens when either of the following are true:
// - resendAll is true
// - there is no entry in notificationStatus (which means this notification was never delivered)
// - there is an entry in notificationStatus but the status is not set to delivered
func doSendNotification(n *libsveltosv1alpha1.Notification, notificationStatus map[string]libsveltosv1alpha1.NotificationStatus,
	resendAll bool) bool {

	if resendAll {
		return true
	}

	status, ok := notificationStatus[n.Name]
	if !ok {
		return true
	}

	return status != libsveltosv1alpha1.NotificationStatusDelivered
}
