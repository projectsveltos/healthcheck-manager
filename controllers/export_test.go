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

var (
	RequeueClusterHealthCheckForCluster = (*ClusterHealthCheckReconciler).requeueClusterHealthCheckForCluster
	RequeueClusterHealthCheckForMachine = (*ClusterHealthCheckReconciler).requeueClusterHealthCheckForMachine

	CleanMaps               = (*ClusterHealthCheckReconciler).cleanMaps
	UpdateMaps              = (*ClusterHealthCheckReconciler).updateMaps
	GetReferenceMapForEntry = (*ClusterHealthCheckReconciler).getReferenceMapForEntry
	GetClusterMapForEntry   = (*ClusterHealthCheckReconciler).getClusterMapForEntry

	ProcessClusterHealthCheck = (*ClusterHealthCheckReconciler).processClusterHealthCheck
	IsClusterEntryRemoved     = (*ClusterHealthCheckReconciler).isClusterEntryRemoved
	UpdateClusterConditions   = (*ClusterHealthCheckReconciler).updateClusterConditions
)

var (
	UpdateEnvs                 = (*ReloaderReportReconciler).updateEnvs
	FetchAndPrepareDeployment  = (*ReloaderReportReconciler).fetchAndPrepareDeployment
	FetchAndPrepareStatefulSet = (*ReloaderReportReconciler).fetchAndPrepareStatefulSet
	FetchAndPrepareDaemonSet   = (*ReloaderReportReconciler).fetchAndPrepareDaemonSet
	TriggerRollingUpgrade      = (*ReloaderReportReconciler).triggerRollingUpgrade
	ProcessReloaderReport      = (*ReloaderReportReconciler).processReloaderReport
)

const (
	SveltosEnv = sveltosEnv
)

var (
	GetKeyFromObject = getKeyFromObject

	GetConditionStatus           = getConditionStatus
	GetConditionType             = getConditionType
	AreAddonsDeployed            = areAddonsDeployed
	FetchClusterSummaries        = fetchClusterSummaries
	HasLivenessCheckStatusChange = hasLivenessCheckStatusChange
	EvaluateLivenessCheckAddOns  = evaluateLivenessCheckAddOns
	EvaluateLivenessCheck        = evaluateLivenessCheck

	DoSendNotification         = doSendNotification
	BuildNotificationStatusMap = buildNotificationStatusMap

	RemoveConditionEntry                  = removeConditionEntry
	UpdateConditionsForCluster            = updateConditionsForCluster
	UpdateNotificationSummariesForCluster = updateNotificationSummariesForCluster
	IsClusterConditionForCluster          = isClusterConditionForCluster
	EvaluateClusterHealthCheckForCluster  = evaluateClusterHealthCheckForCluster
	DeployHealthChecks                    = deployHealthChecks
	RemoveStaleHealthChecks               = removeStaleHealthChecks
	GetReferencedHealthChecks             = getReferencedHealthChecks
)

var (
	RemoveHealthCheckReports                       = removeHealthCheckReports
	RemoveHealthCheckReportsFromCluster            = removeHealthCheckReportsFromCluster
	CollectAndProcessHealthCheckReportsFromCluster = collectAndProcessHealthCheckReportsFromCluster
)

var (
	CollectAndProcessReloaderReportsFromCluster = collectAndProcessReloaderReportsFromCluster
)

var (
	GetSlackInfo = getSlackInfo
	GetWebexInfo = getWebexInfo
)

func GetWebexRoom(info *webexInfo) string {
	return info.room
}
func GetWebexToken(info *webexInfo) string {
	return info.token
}

func GetSlackChannelID(info *slackInfo) string {
	return info.channelID
}
func GetSlackToken(info *slackInfo) string {
	return info.token
}
