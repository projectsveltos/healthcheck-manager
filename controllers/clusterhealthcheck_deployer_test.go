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
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/go-logr/logr"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2/textlogger"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/cluster-api/util"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/projectsveltos/healthcheck-manager/controllers"
	"github.com/projectsveltos/healthcheck-manager/pkg/scope"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
	"github.com/projectsveltos/libsveltos/lib/deployer"
	fakedeployer "github.com/projectsveltos/libsveltos/lib/deployer/fake"
	"github.com/projectsveltos/libsveltos/lib/k8s_utils"
	"github.com/projectsveltos/libsveltos/lib/mgmtagent"
	"github.com/projectsveltos/libsveltos/lib/pullmode"
	libsveltosset "github.com/projectsveltos/libsveltos/lib/set"
)

const (
	ClusterKind = "Cluster"
)

var _ = Describe("ClusterHealthCheck deployer", func() {
	var logger logr.Logger

	BeforeEach(func() {
		logger = textlogger.NewLogger(textlogger.NewConfig(textlogger.Verbosity(1)))
	})

	It("evaluateClusterHealthCheckForCluster ", func() {
		clusterNamespace := randomString()
		clusterName := randomString()
		clusterType := libsveltosv1beta1.ClusterTypeSveltos

		// Following creates a ClusterSummary and an empty ClusterHealthCheck
		c := prepareClientWithClusterSummaryAndCHC(clusterNamespace, clusterName, clusterType)

		// Verify clusterHealthCheck has been created
		chcs := &libsveltosv1beta1.ClusterHealthCheckList{}
		Expect(c.List(context.TODO(), chcs)).To(Succeed())
		Expect(len(chcs.Items)).To(Equal(1))

		Expect(c.List(context.TODO(), chcs)).To(Succeed())
		Expect(len(chcs.Items)).To(Equal(1))

		// Because ClusterSummary has been created with all add-ons provisioned, expect:
		// - passing to be true
		// - condition status to be true
		Expect(len(chcs.Items[0].Spec.LivenessChecks)).To(Equal(1))
		livenessCheck := chcs.Items[0].Spec.LivenessChecks[0]
		conditions, passing, err := controllers.EvaluateClusterHealthCheckForCluster(context.TODO(), c, clusterNamespace, clusterName,
			clusterType, &chcs.Items[0], logger)
		Expect(err).To(BeNil())
		Expect(passing).To(BeTrue())
		Expect(conditions).ToNot(BeNil())
		Expect(len(conditions)).To(Equal(1))
		Expect(conditions[0].Status).To(Equal(corev1.ConditionTrue))
		Expect(conditions[0].Type).To(Equal(libsveltosv1beta1.ConditionType(controllers.GetConditionType(&livenessCheck))))
	})

	It("evaluateAndNotifyProvisionedClusters re-evaluates a cluster already Provisioned", func() {
		// Regression test: conditions used to only be (re-)evaluated for clusters that had *just*
		// transitioned to Provisioned in the current reconcile. A cluster that was already
		// Provisioned before this reconcile started never got its liveness conditions refreshed
		// again, so ClusterHealthCheck.Status.Conditions froze at whatever it was on first
		// provisioning, no matter how the underlying HealthCheckReport/ClusterSummary changed later.
		clusterNamespace := randomString()
		clusterName := randomString()

		// evaluateAndNotifyProvisionedClusters evaluates against the management cluster client
		// (testEnv), not a fake client, so the HealthCheckReport must exist there. An empty
		// ResourceStatuses means healthy (isStatusHealthy, liveness.go).
		healthCheckReportClusterType := libsveltosv1beta1.ClusterTypeCapi
		healthCheckName := randomString()
		healthCheckReport := getHealthCheckReport(healthCheckName, clusterNamespace, clusterName)
		healthCheckReport.Namespace = clusterNamespace
		// fetchHealthCheckReports (liveness.go) matches on the full label set, not just
		// HealthCheckNameLabel (which is all getHealthCheckReport sets by default).
		healthCheckReport.Labels = libsveltosv1beta1.GetHealthCheckReportLabels(
			healthCheckName, clusterName, &healthCheckReportClusterType)

		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: clusterNamespace}}
		Expect(testEnv.Create(context.TODO(), ns)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, ns)).To(Succeed())
		Expect(testEnv.Create(context.TODO(), healthCheckReport)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, healthCheckReport)).To(Succeed())

		chc := &libsveltosv1beta1.ClusterHealthCheck{
			ObjectMeta: metav1.ObjectMeta{
				Name: randomString(),
			},
			Spec: libsveltosv1beta1.ClusterHealthCheckSpec{
				LivenessChecks: []libsveltosv1beta1.LivenessCheck{
					{
						Name: randomString(),
						Type: libsveltosv1beta1.LivenessTypeHealthCheck,
						LivenessSourceRef: &corev1.ObjectReference{
							Name:       healthCheckName,
							Kind:       libsveltosv1beta1.HealthCheckKind,
							APIVersion: libsveltosv1beta1.GroupVersion.String(),
						},
					},
				},
			},
			Status: libsveltosv1beta1.ClusterHealthCheckStatus{
				ClusterConditions: []libsveltosv1beta1.ClusterCondition{
					{
						ClusterInfo: libsveltosv1beta1.ClusterInfo{
							Cluster: corev1.ObjectReference{
								Kind: ClusterKind, APIVersion: clusterv1.GroupVersion.String(),
								Namespace: clusterNamespace, Name: clusterName,
							},
							// Already Provisioned *before* this call: this is the case that was
							// previously skipped.
							Status: libsveltosv1beta1.SveltosStatusProvisioned,
						},
					},
				},
			},
		}

		clusterConditions, err := controllers.EvaluateAndNotifyProvisionedClusters(
			&controllers.ClusterHealthCheckReconciler{Client: testEnv.Client}, context.TODO(), chc, logger)
		Expect(err).To(BeNil())
		Expect(clusterConditions).To(HaveLen(1))
		Expect(clusterConditions[0].Conditions).ToNot(BeEmpty())
		Expect(clusterConditions[0].Conditions[0].Status).To(Equal(corev1.ConditionTrue))
	})

	It("evaluateAndNotifyProvisionedClusters does not demote status on a notification-only failure",
		func() {
			// Regression test: a notification failing to deliver (e.g. a bad webhook secret) is not
			// a deployment failure. It must not overwrite ClusterInfo.Status/FailureMessage (which
			// would make processClusterHealthCheck redeploy the HealthCheck on the next reconcile
			// for no reason), and the liveness Conditions/NotificationSummaries that were
			// successfully computed must still be persisted, not silently dropped.
			clusterNamespace := randomString()
			clusterName := randomString()

			healthCheckReportClusterType := libsveltosv1beta1.ClusterTypeCapi
			healthCheckName := randomString()
			healthCheckReport := getHealthCheckReport(healthCheckName, clusterNamespace, clusterName)
			healthCheckReport.Namespace = clusterNamespace
			healthCheckReport.Labels = libsveltosv1beta1.GetHealthCheckReportLabels(
				healthCheckName, clusterName, &healthCheckReportClusterType)

			ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: clusterNamespace}}
			Expect(testEnv.Create(context.TODO(), ns)).To(Succeed())
			Expect(waitForObject(context.TODO(), testEnv.Client, ns)).To(Succeed())
			Expect(testEnv.Create(context.TODO(), healthCheckReport)).To(Succeed())
			Expect(waitForObject(context.TODO(), testEnv.Client, healthCheckReport)).To(Succeed())

			chc := &libsveltosv1beta1.ClusterHealthCheck{
				ObjectMeta: metav1.ObjectMeta{
					Name: randomString(),
				},
				Spec: libsveltosv1beta1.ClusterHealthCheckSpec{
					LivenessChecks: []libsveltosv1beta1.LivenessCheck{
						{
							Name: randomString(),
							Type: libsveltosv1beta1.LivenessTypeHealthCheck,
							LivenessSourceRef: &corev1.ObjectReference{
								Name:       healthCheckName,
								Kind:       libsveltosv1beta1.HealthCheckKind,
								APIVersion: libsveltosv1beta1.GroupVersion.String(),
							},
						},
					},
					// NotificationRef points at a Secret that does not exist: sendSlackNotification
					// will fail to deliver, without conditions ever being nil.
					Notifications: []libsveltosv1beta1.Notification{
						{
							Name: randomString(),
							Type: libsveltosv1beta1.NotificationTypeSlack,
							NotificationRef: &corev1.ObjectReference{
								Kind:       secretKind,
								APIVersion: coreAPIVersion,
								Namespace:  clusterNamespace,
								Name:       randomString(),
							},
						},
					},
				},
				Status: libsveltosv1beta1.ClusterHealthCheckStatus{
					ClusterConditions: []libsveltosv1beta1.ClusterCondition{
						{
							ClusterInfo: libsveltosv1beta1.ClusterInfo{
								Cluster: corev1.ObjectReference{
									Kind: ClusterKind, APIVersion: clusterv1.GroupVersion.String(),
									Namespace: clusterNamespace, Name: clusterName,
								},
								Status: libsveltosv1beta1.SveltosStatusProvisioned,
							},
						},
					},
				},
			}

			clusterConditions, err := controllers.EvaluateAndNotifyProvisionedClusters(
				&controllers.ClusterHealthCheckReconciler{Client: testEnv.Client}, context.TODO(), chc, logger)
			Expect(err).ToNot(BeNil())
			Expect(clusterConditions).To(HaveLen(1))

			// Deployment status must be untouched by a notification-only failure.
			Expect(clusterConditions[0].ClusterInfo.Status).To(Equal(libsveltosv1beta1.SveltosStatusProvisioned))
			Expect(clusterConditions[0].ClusterInfo.FailureMessage).To(BeNil())

			// Liveness evaluation succeeded and must still be persisted.
			Expect(clusterConditions[0].Conditions).ToNot(BeEmpty())
			Expect(clusterConditions[0].Conditions[0].Status).To(Equal(corev1.ConditionTrue))

			// The notification failure itself must be visible per-channel.
			Expect(clusterConditions[0].NotificationSummaries).To(HaveLen(1))
			Expect(clusterConditions[0].NotificationSummaries[0].Status).
				To(Equal(libsveltosv1beta1.NotificationStatusFailedToDeliver))
			Expect(clusterConditions[0].NotificationSummaries[0].FailureMessage).ToNot(BeNil())
		})

	It("evaluateAndNotifyProvisionedClusters evaluates only the clusters the instance is a shard match for",
		func() {
			// When sharding is used, more instances watch the same ClusterHealthCheck. Only the one
			// owning the shard of a cluster evaluates its liveness checks and delivers notifications.
			// Otherwise each would take decisions from its own, possibly stale, copy of what was
			// already delivered.
			const shardKey = "shard1"

			clusterNamespace := randomString()
			clusterName := randomString()

			healthCheckReportClusterType := libsveltosv1beta1.ClusterTypeCapi
			healthCheckName := randomString()
			healthCheckReport := getHealthCheckReport(healthCheckName, clusterNamespace, clusterName)
			healthCheckReport.Namespace = clusterNamespace
			healthCheckReport.Labels = libsveltosv1beta1.GetHealthCheckReportLabels(
				healthCheckName, clusterName, &healthCheckReportClusterType)

			ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: clusterNamespace}}
			Expect(testEnv.Create(context.TODO(), ns)).To(Succeed())
			Expect(waitForObject(context.TODO(), testEnv.Client, ns)).To(Succeed())

			cluster := &clusterv1.Cluster{
				ObjectMeta: metav1.ObjectMeta{
					Namespace:   clusterNamespace,
					Name:        clusterName,
					Annotations: map[string]string{libsveltosv1beta1.ShardAnnotation: shardKey},
				},
			}
			Expect(testEnv.Create(context.TODO(), cluster)).To(Succeed())
			Expect(waitForObject(context.TODO(), testEnv.Client, cluster)).To(Succeed())

			Expect(testEnv.Create(context.TODO(), healthCheckReport)).To(Succeed())
			Expect(waitForObject(context.TODO(), testEnv.Client, healthCheckReport)).To(Succeed())

			chc := &libsveltosv1beta1.ClusterHealthCheck{
				ObjectMeta: metav1.ObjectMeta{
					Name: randomString(),
				},
				Spec: libsveltosv1beta1.ClusterHealthCheckSpec{
					LivenessChecks: []libsveltosv1beta1.LivenessCheck{
						{
							Name: randomString(),
							Type: libsveltosv1beta1.LivenessTypeHealthCheck,
							LivenessSourceRef: &corev1.ObjectReference{
								Name:       healthCheckName,
								Kind:       libsveltosv1beta1.HealthCheckKind,
								APIVersion: libsveltosv1beta1.GroupVersion.String(),
							},
						},
					},
					// NotificationRef points at a Secret that does not exist: whoever tries to deliver
					// the notification gets an error and a FailedToDeliver summary.
					Notifications: []libsveltosv1beta1.Notification{
						{
							Name: randomString(),
							Type: libsveltosv1beta1.NotificationTypeSlack,
							NotificationRef: &corev1.ObjectReference{
								Kind:       secretKind,
								APIVersion: coreAPIVersion,
								Namespace:  clusterNamespace,
								Name:       randomString(),
							},
						},
					},
				},
				Status: libsveltosv1beta1.ClusterHealthCheckStatus{
					ClusterConditions: []libsveltosv1beta1.ClusterCondition{
						{
							ClusterInfo: libsveltosv1beta1.ClusterInfo{
								Cluster: corev1.ObjectReference{
									Kind: ClusterKind, APIVersion: clusterv1.GroupVersion.String(),
									Namespace: clusterNamespace, Name: clusterName,
								},
								Status: libsveltosv1beta1.SveltosStatusProvisioned,
							},
						},
					},
				},
			}

			By("An instance that is not a shard match for the cluster does not evaluate it, nor notify")
			notOwner := &controllers.ClusterHealthCheckReconciler{Client: testEnv.Client, ShardKey: ""}
			clusterConditions, err := controllers.EvaluateAndNotifyProvisionedClusters(
				notOwner, context.TODO(), chc, logger)
			Expect(err).To(BeNil())
			Expect(clusterConditions).To(HaveLen(1))
			Expect(clusterConditions[0].Conditions).To(BeEmpty())
			Expect(clusterConditions[0].NotificationSummaries).To(BeEmpty())
			Expect(clusterConditions[0].ClusterInfo.Status).To(Equal(libsveltosv1beta1.SveltosStatusProvisioned))

			By("The instance owning the shard of the cluster evaluates it and notifies")
			owner := &controllers.ClusterHealthCheckReconciler{Client: testEnv.Client, ShardKey: shardKey}
			clusterConditions, err = controllers.EvaluateAndNotifyProvisionedClusters(
				owner, context.TODO(), chc, logger)
			Expect(err).ToNot(BeNil())
			Expect(clusterConditions).To(HaveLen(1))
			Expect(clusterConditions[0].Conditions).ToNot(BeEmpty())
			Expect(clusterConditions[0].NotificationSummaries).To(HaveLen(1))
			Expect(clusterConditions[0].NotificationSummaries[0].Status).
				To(Equal(libsveltosv1beta1.NotificationStatusFailedToDeliver))
		})

	It("processClusterHealthCheck queues job", func() {
		clusterNamespace := randomString()
		clusterName := randomString()
		clusterType := libsveltosv1beta1.ClusterTypeCapi

		// Following creates a ClusterSummary and an empty ClusterHealthCheck
		c := prepareClientWithClusterSummaryAndCHC(clusterNamespace, clusterName, clusterType)

		// Add machine to mark Cluster ready
		cpMachine := &clusterv1.Machine{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: clusterNamespace,
				Name:      randomString(),
				Labels: map[string]string{
					clusterv1.ClusterNameLabel:         clusterName,
					clusterv1.MachineControlPlaneLabel: machineControlPlaneLabelValue,
				},
			},
		}
		cpMachine.Status.SetTypedPhase(clusterv1.MachinePhaseRunning)

		Expect(c.Create(context.TODO(), cpMachine)).To(Succeed())

		// Verify clusterHealthCheck has been created
		chcs := &libsveltosv1beta1.ClusterHealthCheckList{}
		Expect(c.List(context.TODO(), chcs)).To(Succeed())
		Expect(len(chcs.Items)).To(Equal(1))

		chc := chcs.Items[0]

		dep := fakedeployer.GetClient(context.TODO(), logger, testEnv.Client)
		controllers.RegisterFeatures(dep, logger)

		reconciler := controllers.ClusterHealthCheckReconciler{
			Client:              c,
			Deployer:            dep,
			Scheme:              c.Scheme(),
			Mux:                 sync.Mutex{},
			ClusterMap:          make(map[corev1.ObjectReference]*libsveltosset.Set),
			CHCToClusterMap:     make(map[types.NamespacedName]*libsveltosset.Set),
			ClusterHealthChecks: make(map[corev1.ObjectReference]libsveltosv1beta1.Selector),
			HealthCheckMap:      make(map[corev1.ObjectReference]*libsveltosset.Set),
			CHCToHealthCheckMap: make(map[types.NamespacedName]*libsveltosset.Set),
		}

		chcScope, err := scope.NewClusterHealthCheckScope(scope.ClusterHealthCheckScopeParams{
			Client:             c,
			Logger:             logger,
			ClusterHealthCheck: &chc,
			ControllerName:     controllerName,
		})
		Expect(err).To(BeNil())

		currentCluster := &clusterv1.Cluster{}
		Expect(c.Get(context.TODO(), types.NamespacedName{Namespace: clusterNamespace, Name: clusterName}, currentCluster)).To(Succeed())
		Expect(addTypeInformationToObject(c.Scheme(), currentCluster)).To(Succeed())

		f := controllers.GetHandlersForFeature(libsveltosv1beta1.FeatureClusterHealthCheck)
		clusterInfo, err := controllers.ProcessClusterHealthCheck(&reconciler, context.TODO(), chcScope,
			controllers.GetKeyFromObject(c.Scheme(), currentCluster), f, logger)
		Expect(err).To(BeNil())

		Expect(clusterInfo).ToNot(BeNil())
		Expect(clusterInfo.Status).To(Equal(libsveltosv1beta1.SveltosStatusProvisioning))

		// Expect job to be queued
		Expect(dep.IsInProgress(clusterNamespace, clusterName, chc.Name, libsveltosv1beta1.FeatureClusterHealthCheck,
			clusterType, false)).To(BeTrue())
	})

	It("deployHealthChecks deploys healthChecks", func() {
		healthCheck := &libsveltosv1beta1.HealthCheck{
			ObjectMeta: metav1.ObjectMeta{
				Name: randomString(),
			},
			Spec: libsveltosv1beta1.HealthCheckSpec{
				ResourceSelectors: []libsveltosv1beta1.ResourceSelector{
					{
						Kind:    randomString(),
						Group:   randomString(),
						Version: randomString(),
					},
				},
				EvaluateHealth: randomString(),
			},
		}

		Expect(testEnv.Create(context.TODO(), healthCheck)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, healthCheck)).To(Succeed())

		clusterNamespace := randomString()
		clusterName := randomString()
		clusterType := libsveltosv1beta1.ClusterTypeCapi

		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: clusterNamespace,
			},
		}
		Expect(testEnv.Create(context.TODO(), ns)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, ns)).To(Succeed())

		cluster := &clusterv1.Cluster{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: clusterNamespace,
				Name:      clusterName,
			},
		}
		Expect(testEnv.Create(context.TODO(), cluster)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, cluster)).To(Succeed())

		chc := &libsveltosv1beta1.ClusterHealthCheck{
			ObjectMeta: metav1.ObjectMeta{
				Name: randomString(),
			},
			Spec: libsveltosv1beta1.ClusterHealthCheckSpec{
				LivenessChecks: []libsveltosv1beta1.LivenessCheck{
					{
						Name: randomString(),
						Type: libsveltosv1beta1.LivenessTypeHealthCheck,
						LivenessSourceRef: &corev1.ObjectReference{
							APIVersion: libsveltosv1beta1.GroupVersion.String(),
							Kind:       libsveltosv1beta1.HealthCheckKind,
							Name:       healthCheck.Name,
						},
					},
				},
				Notifications: []libsveltosv1beta1.Notification{
					{
						Name: randomString(),
						Type: libsveltosv1beta1.NotificationTypeKubernetesEvent,
					},
				},
			},
			Status: libsveltosv1beta1.ClusterHealthCheckStatus{
				MatchingClusterRefs: []corev1.ObjectReference{
					{
						Kind: ClusterKind, APIVersion: clusterv1.GroupVersion.String(), Namespace: clusterNamespace, Name: clusterName,
					},
				},
				ClusterConditions: []libsveltosv1beta1.ClusterCondition{},
			},
		}

		Expect(testEnv.Create(context.TODO(), chc)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, chc)).To(Succeed())

		Expect(addTypeInformationToObject(scheme, chc)).To(Succeed())

		createSecretWithKubeconfig(clusterNamespace, clusterName)

		// deployHealthChecks creates referenced HealthCheck in the managed cluster.
		// We are using testEnv as both management cluster (where this test has already created healthCheck)
		// and managed cluster (where healthCheck is supposed to be created).
		// Existence of healthCheck does not verify deployHealthChecks. But deployHealthCheck is also supposed
		// to add ClusterHealthCheck as OwnerReference of HealthCheck and annotation. So test verifies that.
		Expect(controllers.DeployHealthChecks(context.TODO(), testEnv.Client, clusterNamespace, clusterName,
			clusterType, chc, deployer.Options{}, logger)).To(Succeed())

		Eventually(func() bool {
			currentHealthCheck := &libsveltosv1beta1.HealthCheck{}
			err := testEnv.Get(context.TODO(), types.NamespacedName{Name: healthCheck.Name}, currentHealthCheck)
			if err != nil {
				return false
			}

			targetGK := schema.GroupKind{
				Group: libsveltosv1beta1.GroupVersion.Group,
				Kind:  libsveltosv1beta1.ClusterHealthCheckKind,
			}

			if !util.IsOwnedByObject(currentHealthCheck, chc, targetGK) {
				return false
			}
			if currentHealthCheck.Annotations == nil {
				return false
			}
			if _, ok := currentHealthCheck.Annotations[libsveltosv1beta1.DeployedBySveltosAnnotation]; !ok {
				return false
			}
			return true
		}, timeout, pollingInterval).Should(BeTrue())
	})

	It("deployHealthChecks in pull mode stages every healthCheck referenced by the ClusterHealthCheck", func() {
		clusterNamespace, clusterName := createPullModeSveltosCluster()
		healthChecks, livenessChecks := createHealthChecksWithLivenessChecks(2)
		chc := createClusterHealthCheck(livenessChecks)

		Expect(controllers.DeployHealthChecks(context.TODO(), testEnv.Client, clusterNamespace, clusterName,
			libsveltosv1beta1.ClusterTypeSveltos, chc, deployer.Options{}, logger)).To(Succeed())

		// In pull mode the agent in the managed cluster deploys what the ConfigurationGroup references:
		// there must be one ConfigurationBundle per healthCheck
		configurationGroups := &libsveltosv1beta1.ConfigurationGroupList{}
		Eventually(func() int {
			err := testEnv.List(context.TODO(), configurationGroups, client.InNamespace(clusterNamespace))
			if err != nil || len(configurationGroups.Items) != 1 {
				return -1
			}
			return len(configurationGroups.Items[0].Spec.ConfigurationItems)
		}, timeout, pollingInterval).Should(Equal(len(healthChecks)))

		stagedHealthCheckNames := []string{}
		for _, item := range configurationGroups.Items[0].Spec.ConfigurationItems {
			bundle := &libsveltosv1beta1.ConfigurationBundle{}
			Expect(testEnv.Get(context.TODO(),
				types.NamespacedName{Namespace: clusterNamespace, Name: item.ContentRef.Name}, bundle)).To(Succeed())
			Expect(bundle.Spec.Resources).To(HaveLen(1))

			for _, healthCheck := range healthChecks {
				if strings.Contains(bundle.Spec.Resources[0], healthCheck.Name) {
					stagedHealthCheckNames = append(stagedHealthCheckNames, healthCheck.Name)
				}
			}
		}
		Expect(stagedHealthCheckNames).To(ConsistOf(healthChecks[0].Name, healthChecks[1].Name))
	})

	It("deployHealthChecks in pull mode removes the configurationBundles staged by older versions", func() {
		clusterNamespace, clusterName := createPullModeSveltosCluster()
		healthChecks, livenessChecks := createHealthChecksWithLivenessChecks(1)
		chc := createClusterHealthCheck(livenessChecks)

		legacyBundleName := stageLegacyConfigurationBundle(clusterNamespace, clusterName, chc, healthChecks[0])

		Expect(controllers.DeployHealthChecks(context.TODO(), testEnv.Client, clusterNamespace, clusterName,
			libsveltosv1beta1.ClusterTypeSveltos, chc, deployer.Options{}, logger)).To(Succeed())

		// The bundle staged by the older version is not referenced by any ConfigurationGroup: it must be removed
		Eventually(func() bool {
			bundle := &libsveltosv1beta1.ConfigurationBundle{}
			err := testEnv.Get(context.TODO(), types.NamespacedName{Namespace: clusterNamespace, Name: legacyBundleName}, bundle)
			return apierrors.IsNotFound(err)
		}, timeout, pollingInterval).Should(BeTrue())

		// Only the bundle just staged is left, and the ConfigurationGroup references it
		Eventually(func() bool {
			bundles := listConfigurationBundles(clusterNamespace)
			groups := listConfigurationGroups(clusterNamespace)
			return len(bundles) == 1 && len(groups) == 1 && len(groups[0].Spec.ConfigurationItems) == 1 &&
				groups[0].Spec.ConfigurationItems[0].ContentRef.Name == bundles[0].Name
		}, timeout, pollingInterval).Should(BeTrue())
	})

	It("undeployClusterHealthCheckInPullMode requests the removal when there is no ConfigurationGroup, "+
		"then waits for the agent", func() {

		clusterNamespace, clusterName := createPullModeSveltosCluster()
		_, livenessChecks := createHealthChecksWithLivenessChecks(1)
		chc := createClusterHealthCheck(livenessChecks)

		// No ConfigurationGroup exists. The removal is requested, and a missing group is not reported as a failure
		err := controllers.UndeployClusterHealthCheckInPullMode(context.TODO(), testEnv.Client, clusterNamespace,
			clusterName, chc, logger)
		Expect(err).To(HaveOccurred())
		Expect(apierrors.IsNotFound(err)).To(BeFalse())
		Expect(err.Error()).To(ContainSubstring("agent cleanup request is queued"))

		Eventually(func() bool {
			groups := listConfigurationGroups(clusterNamespace)
			return len(groups) == 1 && groups[0].Spec.Action == libsveltosv1beta1.ActionRemove
		}, timeout, pollingInterval).Should(BeTrue())

		// The agent reports the resources removed
		removed := libsveltosv1beta1.FeatureStatusRemoved
		Eventually(func() error {
			groups := listConfigurationGroups(clusterNamespace)
			if len(groups) != 1 {
				return errors.New("ConfigurationGroup not found")
			}
			groups[0].Status.DeploymentStatus = &removed
			return testEnv.Status().Update(context.TODO(), &groups[0])
		}, timeout, pollingInterval).Should(Succeed())

		// Once the agent removed the resources, the ConfigurationGroup is deleted
		Eventually(func() error {
			return controllers.UndeployClusterHealthCheckInPullMode(context.TODO(), testEnv.Client,
				clusterNamespace, clusterName, chc, logger)
		}, timeout, pollingInterval).Should(Succeed())

		Eventually(func() int {
			return len(listConfigurationGroups(clusterNamespace))
		}, timeout, pollingInterval).Should(BeZero())
	})

	It("undeployClusterHealthCheckInPullMode requests the removal when the ConfigurationGroup is still the one "+
		"used to deploy", func() {

		clusterNamespace, clusterName := createPullModeSveltosCluster()
		_, livenessChecks := createHealthChecksWithLivenessChecks(1)
		chc := createClusterHealthCheck(livenessChecks)

		Expect(controllers.DeployHealthChecks(context.TODO(), testEnv.Client, clusterNamespace, clusterName,
			libsveltosv1beta1.ClusterTypeSveltos, chc, deployer.Options{}, logger)).To(Succeed())

		Eventually(func() bool {
			groups := listConfigurationGroups(clusterNamespace)
			return len(groups) == 1 && groups[0].Spec.Action == libsveltosv1beta1.ActionDeploy
		}, timeout, pollingInterval).Should(BeTrue())

		// The agent has not confirmed the deployment yet: the ConfigurationGroup action is not Remove. This is not
		// a failure, the removal is requested
		err := controllers.UndeployClusterHealthCheckInPullMode(context.TODO(), testEnv.Client, clusterNamespace,
			clusterName, chc, logger)
		Expect(err).To(HaveOccurred())
		Expect(pullmode.IsActionNotSetToRemove(err)).To(BeFalse())
		Expect(err.Error()).To(ContainSubstring("agent cleanup request is queued"))

		Eventually(func() bool {
			groups := listConfigurationGroups(clusterNamespace)
			return len(groups) == 1 && groups[0].Spec.Action == libsveltosv1beta1.ActionRemove
		}, timeout, pollingInterval).Should(BeTrue())
	})

	It("undeployClusterHealthCheckInPullMode removes the configurationBundles", func() {
		clusterNamespace, clusterName := createPullModeSveltosCluster()
		_, livenessChecks := createHealthChecksWithLivenessChecks(2)
		chc := createClusterHealthCheck(livenessChecks)

		Expect(controllers.DeployHealthChecks(context.TODO(), testEnv.Client, clusterNamespace, clusterName,
			libsveltosv1beta1.ClusterTypeSveltos, chc, deployer.Options{}, logger)).To(Succeed())

		Eventually(func() bool {
			return len(listConfigurationBundles(clusterNamespace)) == len(livenessChecks) &&
				len(listConfigurationGroups(clusterNamespace)) == 1
		}, timeout, pollingInterval).Should(BeTrue())

		// The agent has not removed anything yet, so an error is expected
		Expect(controllers.UndeployClusterHealthCheckInPullMode(context.TODO(), testEnv.Client, clusterNamespace,
			clusterName, chc, logger)).ToNot(Succeed())

		// The ConfigurationGroup does not reference any bundle anymore. Nothing else does.
		Eventually(func() int {
			return len(listConfigurationBundles(clusterNamespace))
		}, timeout, pollingInterval).Should(BeZero())
	})

	It("removeReportsOfReferencedHealthChecks removes the HealthCheckReports of the referenced HealthChecks", func() {
		clusterNamespace, clusterName := createPullModeSveltosCluster()
		clusterType := libsveltosv1beta1.ClusterTypeSveltos

		healthChecks, livenessChecks := createHealthChecksWithLivenessChecks(2)
		// A liveness check without a reference, and one which is not a HealthCheck, must be ignored
		livenessChecks = append(livenessChecks,
			libsveltosv1beta1.LivenessCheck{Name: randomString(), Type: libsveltosv1beta1.LivenessTypeHealthCheck},
			libsveltosv1beta1.LivenessCheck{Name: randomString(), Type: libsveltosv1beta1.LivenessTypeAddons})
		chc := createClusterHealthCheck(livenessChecks)

		reportsToRemove := []*libsveltosv1beta1.HealthCheckReport{
			createHealthCheckReport(clusterNamespace, clusterName, healthChecks[0].Name, clusterType),
			createHealthCheckReport(clusterNamespace, clusterName, healthChecks[1].Name, clusterType),
		}
		// Report of an HealthCheck this ClusterHealthCheck does not reference
		reportToKeep := createHealthCheckReport(clusterNamespace, clusterName, randomString(), clusterType)

		// The name of a liveness check is not the name of the HealthCheck
		for i := range healthChecks {
			Expect(livenessChecks[i].Name).ToNot(Equal(healthChecks[i].Name))
		}

		Expect(controllers.RemoveReportsOfReferencedHealthChecks(context.TODO(), testEnv.Client, clusterNamespace,
			clusterName, clusterType, chc, logger)).To(Succeed())

		for _, report := range reportsToRemove {
			Eventually(func() bool {
				currentReport := &libsveltosv1beta1.HealthCheckReport{}
				err := testEnv.Get(context.TODO(), client.ObjectKeyFromObject(report), currentReport)
				return apierrors.IsNotFound(err)
			}, timeout, pollingInterval).Should(BeTrue())
		}

		Expect(testEnv.Get(context.TODO(), client.ObjectKeyFromObject(reportToKeep),
			&libsveltosv1beta1.HealthCheckReport{})).To(Succeed())
	})

	It("removeClusterHealthCheck marks the cluster as Removed once the cleanup is completed", func() {
		clusterNamespace, clusterName := createPullModeSveltosCluster()
		_, livenessChecks := createHealthChecksWithLivenessChecks(1)
		chc := createClusterHealthCheck(livenessChecks)

		dep := &removedResultDeployer{DeployerInterface: fakedeployer.GetClient(context.TODO(), logger, testEnv.Client)}
		controllers.RegisterFeatures(dep, logger)

		reconciler := controllers.ClusterHealthCheckReconciler{
			Client:   testEnv.Client,
			Deployer: dep,
			Scheme:   testEnv.Scheme(),
		}

		chcScope, err := scope.NewClusterHealthCheckScope(scope.ClusterHealthCheckScopeParams{
			Client:             testEnv.Client,
			Logger:             logger,
			ClusterHealthCheck: chc,
			ControllerName:     controllerName,
		})
		Expect(err).To(BeNil())

		clusterRef := &corev1.ObjectReference{
			APIVersion: libsveltosv1beta1.GroupVersion.String(),
			Kind:       libsveltosv1beta1.SveltosClusterKind,
			Namespace:  clusterNamespace,
			Name:       clusterName,
		}

		f := controllers.GetHandlersForFeature(libsveltosv1beta1.FeatureClusterHealthCheck)

		// Otherwise the cluster would be kept in the status as Removing, and the removal would start over
		clusterInfo, err := controllers.RemoveClusterHealthCheck(&reconciler, context.TODO(), chcScope, clusterRef,
			[]byte(randomString()), f, logger)
		Expect(err).To(BeNil())
		Expect(clusterInfo).ToNot(BeNil())
		Expect(clusterInfo.Status).To(Equal(libsveltosv1beta1.SveltosStatusRemoved))
	})

	It("removeStaleHealthChecks removes healthCheck deployed by a ClusterHealthCheck and not referenced anymore", func() {
		healthCheck := &libsveltosv1beta1.HealthCheck{
			ObjectMeta: metav1.ObjectMeta{
				Name: randomString(),
			},
			Spec: libsveltosv1beta1.HealthCheckSpec{
				ResourceSelectors: []libsveltosv1beta1.ResourceSelector{
					{
						Kind:    randomString(),
						Group:   randomString(),
						Version: randomString(),
					},
				},
				EvaluateHealth: randomString(),
			},
		}

		Expect(testEnv.Create(context.TODO(), healthCheck)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, healthCheck)).To(Succeed())

		clusterNamespace := randomString()
		clusterName := randomString()
		clusterType := libsveltosv1beta1.ClusterTypeCapi

		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: clusterNamespace,
			},
		}
		Expect(testEnv.Create(context.TODO(), ns)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, ns)).To(Succeed())

		cluster := &clusterv1.Cluster{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: clusterNamespace,
				Name:      clusterName,
			},
		}
		Expect(testEnv.Create(context.TODO(), cluster)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, cluster)).To(Succeed())

		chc := &libsveltosv1beta1.ClusterHealthCheck{
			ObjectMeta: metav1.ObjectMeta{
				Name: randomString(),
			},
			Spec: libsveltosv1beta1.ClusterHealthCheckSpec{
				LivenessChecks: []libsveltosv1beta1.LivenessCheck{
					{
						Name: randomString(),
						Type: libsveltosv1beta1.LivenessTypeHealthCheck,
						LivenessSourceRef: &corev1.ObjectReference{
							APIVersion: libsveltosv1beta1.GroupVersion.Group,
							Kind:       libsveltosv1beta1.HealthCheckKind,
							Name:       randomString(), // make it reference different healthCheck
						},
					},
				},
				Notifications: []libsveltosv1beta1.Notification{
					{
						Name: randomString(),
						Type: libsveltosv1beta1.NotificationTypeKubernetesEvent,
					},
				},
			},
			Status: libsveltosv1beta1.ClusterHealthCheckStatus{
				MatchingClusterRefs: []corev1.ObjectReference{
					{
						Kind: ClusterKind, APIVersion: clusterv1.GroupVersion.String(), Namespace: clusterNamespace, Name: clusterName,
					},
				},
				ClusterConditions: []libsveltosv1beta1.ClusterCondition{},
			},
		}

		Expect(testEnv.Create(context.TODO(), chc)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, chc)).To(Succeed())

		Expect(addTypeInformationToObject(scheme, chc)).To(Succeed())

		// Add ClusterHealthCheck as owner of healthCheck. This indicates previously healthCheck was
		// deployed because of this ClusterHealthCheck instance
		k8s_utils.AddOwnerReference(healthCheck, chc)
		Expect(testEnv.Update(context.TODO(), healthCheck)).To(Succeed())

		createSecretWithKubeconfig(clusterNamespace, clusterName)

		// Test created HealthCheck instance and added ClusterHealthCheck as ownerReference, indicating healthCheck was deployed
		// because of the ClusterHealthCheck instance.
		// Test has ClusterHealthCheck instance reference a different HealthCheck.
		// RemoveStaleHealthChecks will remove the HealthCheck test created.
		Expect(controllers.RemoveStaleHealthChecks(context.TODO(), testEnv.Client, clusterNamespace, clusterName, clusterType,
			chc, false, logger)).To(Succeed())

		Eventually(func() bool {
			currentHealthCheck := &libsveltosv1beta1.HealthCheck{}
			err := testEnv.Get(context.TODO(), types.NamespacedName{Name: healthCheck.Name}, currentHealthCheck)
			if err != nil {
				if apierrors.IsNotFound(err) {
					return true
				}
			}
			return false
		}, timeout, pollingInterval).Should(BeTrue())
	})

	It("removeStaleHealthChecks keeps a healthCheck still referenced when the cluster is still a match", func() {
		clusterNamespace, clusterName, chc, healthCheck := createReferencedHealthCheckOwnedByClusterHealthCheck()

		Expect(controllers.RemoveStaleHealthChecks(context.TODO(), testEnv.Client, clusterNamespace, clusterName,
			libsveltosv1beta1.ClusterTypeCapi, chc, false, logger)).To(Succeed())

		// Read from the API server, not from the cache: a deletion could take a few seconds to be visible there
		currentHealthCheck := &libsveltosv1beta1.HealthCheck{}
		Expect(testEnv.GetAPIReader().Get(context.TODO(), types.NamespacedName{Name: healthCheck.Name},
			currentHealthCheck)).To(Succeed())
		Expect(currentHealthCheck.GetOwnerReferences()).To(HaveLen(1))
	})

	It("removeStaleHealthChecks removes a healthCheck still referenced when the cluster is not a match anymore", func() {
		clusterNamespace, clusterName, chc, healthCheck := createReferencedHealthCheckOwnedByClusterHealthCheck()

		// The ClusterHealthCheck still references the healthCheck, but nothing must be left in a cluster
		// which is not a match anymore
		Expect(controllers.RemoveStaleHealthChecks(context.TODO(), testEnv.Client, clusterNamespace, clusterName,
			libsveltosv1beta1.ClusterTypeCapi, chc, true, logger)).To(Succeed())

		Eventually(func() bool {
			err := testEnv.Get(context.TODO(), types.NamespacedName{Name: healthCheck.Name}, &libsveltosv1beta1.HealthCheck{})
			return apierrors.IsNotFound(err)
		}, timeout, pollingInterval).Should(BeTrue())
	})

	It("removeStaleHealthChecks removes the ConfigMap entries of the referenced healthChecks only when the cluster "+
		"is not a match anymore (sveltos-agent in the management cluster)", func() {

		controllers.SetAgentInMgmtCluster(true)
		defer controllers.SetAgentInMgmtCluster(false)

		clusterNamespace, clusterName, chc, healthCheck := createReferencedHealthCheckOwnedByClusterHealthCheck()
		Expect(controllers.AddHealthCheckToConfigMap(context.TODO(), testEnv.Client, clusterNamespace, clusterName,
			libsveltosv1beta1.ClusterTypeCapi, chc, healthCheck, logger)).To(Succeed())

		entryKey := mgmtagent.GetKeyForHealthCheck(chc.Name, healthCheck.Name)
		configMapKey := types.NamespacedName{
			Namespace: clusterNamespace,
			Name:      mgmtagent.GetConfigMapName(clusterName, libsveltosv1beta1.ClusterTypeCapi),
		}
		getEntries := func() map[string]string {
			configMap := &corev1.ConfigMap{}
			Expect(testEnv.GetAPIReader().Get(context.TODO(), configMapKey, configMap)).To(Succeed())
			return configMap.Data
		}

		// removeStaleHealthChecks reads the ConfigMap from the cache: wait for the cache to have it
		Eventually(func() error {
			return testEnv.Get(context.TODO(), configMapKey, &corev1.ConfigMap{})
		}, timeout, pollingInterval).Should(Succeed())

		// The cluster is still a match and the healthCheck is still referenced: the entry is kept
		Expect(controllers.RemoveStaleHealthChecks(context.TODO(), testEnv.Client, clusterNamespace, clusterName,
			libsveltosv1beta1.ClusterTypeCapi, chc, false, logger)).To(Succeed())
		Expect(getEntries()).To(HaveKey(entryKey))

		Expect(controllers.RemoveStaleHealthChecks(context.TODO(), testEnv.Client, clusterNamespace, clusterName,
			libsveltosv1beta1.ClusterTypeCapi, chc, true, logger)).To(Succeed())
		Expect(getEntries()).ToNot(HaveKey(entryKey))
	})

	It("removeStaleHealthChecks returns an error and keeps the healthCheck when its HealthCheckReports cannot be removed", func() {
		healthCheck := &libsveltosv1beta1.HealthCheck{
			ObjectMeta: metav1.ObjectMeta{
				Name: randomString(),
			},
			Spec: libsveltosv1beta1.HealthCheckSpec{
				ResourceSelectors: []libsveltosv1beta1.ResourceSelector{
					{
						Kind:    randomString(),
						Group:   randomString(),
						Version: randomString(),
					},
				},
				EvaluateHealth: randomString(),
			},
		}

		Expect(testEnv.Create(context.TODO(), healthCheck)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, healthCheck)).To(Succeed())

		clusterNamespace := randomString()
		clusterName := randomString()
		clusterType := libsveltosv1beta1.ClusterTypeCapi

		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: clusterNamespace,
			},
		}
		Expect(testEnv.Create(context.TODO(), ns)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, ns)).To(Succeed())

		cluster := &clusterv1.Cluster{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: clusterNamespace,
				Name:      clusterName,
			},
		}
		Expect(testEnv.Create(context.TODO(), cluster)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, cluster)).To(Succeed())

		healthCheckReport := &libsveltosv1beta1.HealthCheckReport{
			ObjectMeta: metav1.ObjectMeta{
				Name:      randomString(),
				Namespace: clusterNamespace,
				Labels: map[string]string{
					libsveltosv1beta1.HealthCheckNameLabel:              healthCheck.Name,
					libsveltosv1beta1.HealthCheckReportClusterNameLabel: clusterName,
					libsveltosv1beta1.HealthCheckReportClusterTypeLabel: strings.ToLower(string(clusterType)),
				},
			},
			Spec: libsveltosv1beta1.HealthCheckReportSpec{
				HealthCheckName:  healthCheck.Name,
				ClusterNamespace: clusterNamespace,
				ClusterName:      clusterName,
				ClusterType:      clusterType,
			},
		}
		Expect(testEnv.Create(context.TODO(), healthCheckReport)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, healthCheckReport)).To(Succeed())

		chc := &libsveltosv1beta1.ClusterHealthCheck{
			ObjectMeta: metav1.ObjectMeta{
				Name: randomString(),
			},
			Spec: libsveltosv1beta1.ClusterHealthCheckSpec{
				LivenessChecks: []libsveltosv1beta1.LivenessCheck{
					{
						Name: randomString(),
						Type: libsveltosv1beta1.LivenessTypeHealthCheck,
						LivenessSourceRef: &corev1.ObjectReference{
							APIVersion: libsveltosv1beta1.GroupVersion.Group,
							Kind:       libsveltosv1beta1.HealthCheckKind,
							Name:       randomString(), // make it reference different healthCheck
						},
					},
				},
				Notifications: []libsveltosv1beta1.Notification{
					{
						Name: randomString(),
						Type: libsveltosv1beta1.NotificationTypeKubernetesEvent,
					},
				},
			},
		}

		Expect(testEnv.Create(context.TODO(), chc)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, chc)).To(Succeed())

		Expect(addTypeInformationToObject(scheme, chc)).To(Succeed())

		k8s_utils.AddOwnerReference(healthCheck, chc)
		Expect(testEnv.Update(context.TODO(), healthCheck)).To(Succeed())

		createSecretWithKubeconfig(clusterNamespace, clusterName)

		// Deleting a HealthCheckReport fails with an error which is not NotFound
		deleteErr := errors.New("failed to delete HealthCheckReport")
		failingClient := &healthCheckReportDeleteFailingClient{Client: testEnv.Client, err: deleteErr}

		// The HealthCheck must not be considered removed: the error is returned so the removal is retried
		err := controllers.RemoveStaleHealthChecks(context.TODO(), failingClient, clusterNamespace, clusterName,
			clusterType, chc, false, logger)
		Expect(err).To(MatchError(deleteErr))

		currentHealthCheck := &libsveltosv1beta1.HealthCheck{}
		Expect(testEnv.Get(context.TODO(), types.NamespacedName{Name: healthCheck.Name}, currentHealthCheck)).To(Succeed())
		Expect(currentHealthCheck.DeletionTimestamp.IsZero()).To(BeTrue())
	})

	It("removeStaleHealthCheckReports ignores a HealthCheckReport which is already removed", func() {
		clusterNamespace := randomString()
		clusterName := randomString()
		healthCheckName := randomString()
		clusterType := libsveltosv1beta1.ClusterTypeCapi

		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: clusterNamespace,
			},
		}
		Expect(testEnv.Create(context.TODO(), ns)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, ns)).To(Succeed())

		healthCheckReport := &libsveltosv1beta1.HealthCheckReport{
			ObjectMeta: metav1.ObjectMeta{
				Name:      randomString(),
				Namespace: clusterNamespace,
				Labels: map[string]string{
					libsveltosv1beta1.HealthCheckNameLabel:              healthCheckName,
					libsveltosv1beta1.HealthCheckReportClusterNameLabel: clusterName,
					libsveltosv1beta1.HealthCheckReportClusterTypeLabel: strings.ToLower(string(clusterType)),
				},
			},
			Spec: libsveltosv1beta1.HealthCheckReportSpec{
				HealthCheckName:  healthCheckName,
				ClusterNamespace: clusterNamespace,
				ClusterName:      clusterName,
				ClusterType:      clusterType,
			},
		}
		Expect(testEnv.Create(context.TODO(), healthCheckReport)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, healthCheckReport)).To(Succeed())

		// The HealthCheckReport is listed, but it is gone by the time it is deleted
		notFoundErr := apierrors.NewNotFound(schema.GroupResource{
			Group: libsveltosv1beta1.GroupVersion.Group, Resource: "healthcheckreports"}, healthCheckReport.Name)
		raceClient := &healthCheckReportDeleteFailingClient{Client: testEnv.Client, err: notFoundErr}

		Expect(controllers.RemoveStaleHealthCheckReports(context.TODO(), raceClient, clusterNamespace, clusterName,
			healthCheckName, clusterType, logger)).To(Succeed())
	})

	It("getReferencedHealthChecks returns HealthChecks referenced by a ClusterHealthCheck", func() {
		healthCheckName1 := randomString()
		healthCheckName2 := randomString()

		chc := &libsveltosv1beta1.ClusterHealthCheck{
			ObjectMeta: metav1.ObjectMeta{
				Name: randomString(),
			},
			Spec: libsveltosv1beta1.ClusterHealthCheckSpec{
				LivenessChecks: []libsveltosv1beta1.LivenessCheck{
					{
						Name: randomString(),
						Type: libsveltosv1beta1.LivenessTypeHealthCheck,
						LivenessSourceRef: &corev1.ObjectReference{
							APIVersion: libsveltosv1beta1.GroupVersion.Group,
							Kind:       libsveltosv1beta1.HealthCheckKind,
							Name:       healthCheckName1,
						},
					},
					{
						Name: randomString(),
						Type: libsveltosv1beta1.LivenessTypeHealthCheck,
						LivenessSourceRef: &corev1.ObjectReference{
							APIVersion: libsveltosv1beta1.GroupVersion.Group,
							Kind:       libsveltosv1beta1.HealthCheckKind,
							Name:       healthCheckName2,
						},
					},
				},
			},
		}

		referenced := controllers.GetReferencedHealthChecks(chc, logger)

		objRef := &corev1.ObjectReference{
			APIVersion: libsveltosv1beta1.GroupVersion.String(),
			Kind:       libsveltosv1beta1.HealthCheckKind,
			Name:       healthCheckName1,
		}
		Expect(referenced.Has(objRef)).To(BeTrue())

		objRef.Name = healthCheckName2
		Expect(referenced.Has(objRef)).To(BeTrue())

		now := metav1.NewTime(time.Now())
		chc.DeletionTimestamp = &now
		// If ClusterHealthCheck is marked for deletion, treat as if no references
		referenced = controllers.GetReferencedHealthChecks(chc, logger)
		Expect(referenced.Len()).To(BeZero())
	})
})

// healthCheckReportDeleteFailingClient fails any attempt to delete a HealthCheckReport with err
type healthCheckReportDeleteFailingClient struct {
	client.Client
	err error
}

func (c *healthCheckReportDeleteFailingClient) Delete(ctx context.Context, obj client.Object,
	opts ...client.DeleteOption) error {

	if _, ok := obj.(*libsveltosv1beta1.HealthCheckReport); ok {
		return c.err
	}
	return c.Client.Delete(ctx, obj, opts...)
}

// removedResultDeployer reports any cleanup as completed. The fake deployer never returns a Removed result.
type removedResultDeployer struct {
	deployer.DeployerInterface
}

func (d *removedResultDeployer) GetResult(_ context.Context, _, _, _, _ string,
	_ libsveltosv1beta1.ClusterType, _ bool) deployer.Result {

	return deployer.Result{ResultStatus: deployer.Removed}
}

// createPullModeSveltosCluster creates a SveltosCluster in pull mode, in a new namespace
func createPullModeSveltosCluster() (clusterNamespace, clusterName string) {
	clusterNamespace = randomString()
	clusterName = randomString()

	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: clusterNamespace,
		},
	}
	Expect(testEnv.Create(context.TODO(), ns)).To(Succeed())
	Expect(waitForObject(context.TODO(), testEnv.Client, ns)).To(Succeed())

	sveltosCluster := &libsveltosv1beta1.SveltosCluster{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: clusterNamespace,
			Name:      clusterName,
		},
		Spec: libsveltosv1beta1.SveltosClusterSpec{
			PullMode: true,
		},
	}
	Expect(testEnv.Create(context.TODO(), sveltosCluster)).To(Succeed())
	Expect(waitForObject(context.TODO(), testEnv.Client, sveltosCluster)).To(Succeed())

	return clusterNamespace, clusterName
}

// createHealthChecksWithLivenessChecks creates count HealthChecks. It returns them with one liveness check each
// referencing the HealthCheck. The name of a liveness check is different from the name of its HealthCheck.
func createHealthChecksWithLivenessChecks(count int) ([]*libsveltosv1beta1.HealthCheck, []libsveltosv1beta1.LivenessCheck) {
	healthChecks := make([]*libsveltosv1beta1.HealthCheck, count)
	livenessChecks := make([]libsveltosv1beta1.LivenessCheck, count)

	for i := range healthChecks {
		healthChecks[i] = &libsveltosv1beta1.HealthCheck{
			ObjectMeta: metav1.ObjectMeta{
				Name: randomString(),
			},
			Spec: libsveltosv1beta1.HealthCheckSpec{
				ResourceSelectors: []libsveltosv1beta1.ResourceSelector{
					{
						Kind:    randomString(),
						Group:   randomString(),
						Version: randomString(),
					},
				},
				EvaluateHealth: randomString(),
			},
		}
		Expect(testEnv.Create(context.TODO(), healthChecks[i])).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, healthChecks[i])).To(Succeed())

		livenessChecks[i] = libsveltosv1beta1.LivenessCheck{
			Name: randomString(),
			Type: libsveltosv1beta1.LivenessTypeHealthCheck,
			LivenessSourceRef: &corev1.ObjectReference{
				APIVersion: libsveltosv1beta1.GroupVersion.String(),
				Kind:       libsveltosv1beta1.HealthCheckKind,
				Name:       healthChecks[i].Name,
			},
		}
	}

	return healthChecks, livenessChecks
}

// createClusterHealthCheck creates a ClusterHealthCheck with the given liveness checks
func createClusterHealthCheck(livenessChecks []libsveltosv1beta1.LivenessCheck) *libsveltosv1beta1.ClusterHealthCheck {
	chc := &libsveltosv1beta1.ClusterHealthCheck{
		ObjectMeta: metav1.ObjectMeta{
			Name: randomString(),
		},
		Spec: libsveltosv1beta1.ClusterHealthCheckSpec{
			LivenessChecks: livenessChecks,
			Notifications: []libsveltosv1beta1.Notification{
				{
					Name: randomString(),
					Type: libsveltosv1beta1.NotificationTypeKubernetesEvent,
				},
			},
		},
	}
	Expect(testEnv.Create(context.TODO(), chc)).To(Succeed())
	Expect(waitForObject(context.TODO(), testEnv.Client, chc)).To(Succeed())

	Expect(addTypeInformationToObject(scheme, chc)).To(Succeed())

	return chc
}

// stageLegacyConfigurationBundle stages the healthCheck the way older versions did: with HealthCheckReportKind,
// instead of ClusterHealthCheckKind, as requestor kind. It returns the name of the ConfigurationBundle.
func stageLegacyConfigurationBundle(clusterNamespace, clusterName string, chc *libsveltosv1beta1.ClusterHealthCheck,
	healthCheck *libsveltosv1beta1.HealthCheck) string {

	Expect(addTypeInformationToObject(scheme, healthCheck)).To(Succeed())
	unstructuredObj, err := runtime.DefaultUnstructuredConverter.ToUnstructured(healthCheck)
	Expect(err).To(BeNil())
	resources := map[string][]unstructured.Unstructured{
		"healthcheck-instance": {{Object: unstructuredObj}},
	}

	logger := textlogger.NewLogger(textlogger.NewConfig(textlogger.Verbosity(1)))
	Expect(pullmode.StageResourcesForDeployment(context.TODO(), testEnv.Client, clusterNamespace, clusterName,
		libsveltosv1beta1.HealthCheckReportKind, chc.Name, libsveltosv1beta1.FeatureClusterHealthCheck, resources,
		false, logger)).To(Succeed())

	var bundleName string
	Eventually(func() bool {
		bundles := listConfigurationBundles(clusterNamespace)
		if len(bundles) != 1 {
			return false
		}
		bundleName = bundles[0].Name
		return true
	}, timeout, pollingInterval).Should(BeTrue())

	return bundleName
}

func listConfigurationBundles(namespace string) []libsveltosv1beta1.ConfigurationBundle {
	bundles := &libsveltosv1beta1.ConfigurationBundleList{}
	Expect(testEnv.List(context.TODO(), bundles, client.InNamespace(namespace))).To(Succeed())
	return bundles.Items
}

func listConfigurationGroups(namespace string) []libsveltosv1beta1.ConfigurationGroup {
	groups := &libsveltosv1beta1.ConfigurationGroupList{}
	Expect(testEnv.List(context.TODO(), groups, client.InNamespace(namespace))).To(Succeed())
	return groups.Items
}

// createHealthCheckReport creates a HealthCheckReport pulled from the cluster because of the HealthCheck
func createHealthCheckReport(clusterNamespace, clusterName, healthCheckName string,
	clusterType libsveltosv1beta1.ClusterType) *libsveltosv1beta1.HealthCheckReport {

	healthCheckReport := &libsveltosv1beta1.HealthCheckReport{
		ObjectMeta: metav1.ObjectMeta{
			Name:      randomString(),
			Namespace: clusterNamespace,
			Labels: map[string]string{
				libsveltosv1beta1.HealthCheckNameLabel:              healthCheckName,
				libsveltosv1beta1.HealthCheckReportClusterNameLabel: clusterName,
				libsveltosv1beta1.HealthCheckReportClusterTypeLabel: strings.ToLower(string(clusterType)),
			},
		},
		Spec: libsveltosv1beta1.HealthCheckReportSpec{
			HealthCheckName:  healthCheckName,
			ClusterNamespace: clusterNamespace,
			ClusterName:      clusterName,
			ClusterType:      clusterType,
		},
	}
	Expect(testEnv.Create(context.TODO(), healthCheckReport)).To(Succeed())
	Expect(waitForObject(context.TODO(), testEnv.Client, healthCheckReport)).To(Succeed())

	return healthCheckReport
}

// createReferencedHealthCheckOwnedByClusterHealthCheck creates a (push mode) cluster, a HealthCheck and a
// ClusterHealthCheck which references the HealthCheck and is its owner: the HealthCheck was deployed in the
// cluster because of the ClusterHealthCheck.
func createReferencedHealthCheckOwnedByClusterHealthCheck() (clusterNamespace, clusterName string,
	chc *libsveltosv1beta1.ClusterHealthCheck, healthCheck *libsveltosv1beta1.HealthCheck) {

	healthCheck = &libsveltosv1beta1.HealthCheck{
		ObjectMeta: metav1.ObjectMeta{
			Name: randomString(),
		},
		Spec: libsveltosv1beta1.HealthCheckSpec{
			ResourceSelectors: []libsveltosv1beta1.ResourceSelector{
				{
					Kind:    randomString(),
					Group:   randomString(),
					Version: randomString(),
				},
			},
			EvaluateHealth: randomString(),
		},
	}
	Expect(testEnv.Create(context.TODO(), healthCheck)).To(Succeed())
	Expect(waitForObject(context.TODO(), testEnv.Client, healthCheck)).To(Succeed())

	clusterNamespace = randomString()
	clusterName = randomString()

	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: clusterNamespace}}
	Expect(testEnv.Create(context.TODO(), ns)).To(Succeed())
	Expect(waitForObject(context.TODO(), testEnv.Client, ns)).To(Succeed())

	cluster := &clusterv1.Cluster{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: clusterNamespace,
			Name:      clusterName,
		},
	}
	Expect(testEnv.Create(context.TODO(), cluster)).To(Succeed())
	Expect(waitForObject(context.TODO(), testEnv.Client, cluster)).To(Succeed())

	chc = &libsveltosv1beta1.ClusterHealthCheck{
		ObjectMeta: metav1.ObjectMeta{
			Name: randomString(),
		},
		Spec: libsveltosv1beta1.ClusterHealthCheckSpec{
			LivenessChecks: []libsveltosv1beta1.LivenessCheck{
				{
					Name: randomString(),
					Type: libsveltosv1beta1.LivenessTypeHealthCheck,
					LivenessSourceRef: &corev1.ObjectReference{
						APIVersion: libsveltosv1beta1.GroupVersion.String(),
						Kind:       libsveltosv1beta1.HealthCheckKind,
						Name:       healthCheck.Name,
					},
				},
			},
			Notifications: []libsveltosv1beta1.Notification{
				{
					Name: randomString(),
					Type: libsveltosv1beta1.NotificationTypeKubernetesEvent,
				},
			},
		},
	}
	Expect(testEnv.Create(context.TODO(), chc)).To(Succeed())
	Expect(waitForObject(context.TODO(), testEnv.Client, chc)).To(Succeed())
	Expect(addTypeInformationToObject(scheme, chc)).To(Succeed())

	k8s_utils.AddOwnerReference(healthCheck, chc)
	Expect(testEnv.Update(context.TODO(), healthCheck)).To(Succeed())

	createSecretWithKubeconfig(clusterNamespace, clusterName)

	return clusterNamespace, clusterName, chc, healthCheck
}
