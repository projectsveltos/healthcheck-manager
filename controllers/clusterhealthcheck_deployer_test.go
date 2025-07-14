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
	"reflect"
	"sync"
	"time"

	"github.com/go-logr/logr"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2/textlogger"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/cluster-api/util"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/projectsveltos/healthcheck-manager/controllers"
	"github.com/projectsveltos/healthcheck-manager/pkg/scope"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
	"github.com/projectsveltos/libsveltos/lib/deployer"
	fakedeployer "github.com/projectsveltos/libsveltos/lib/deployer/fake"
	"github.com/projectsveltos/libsveltos/lib/k8s_utils"
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

	It("removeConditionEntry removes cluster entry", func() {
		clusterNamespace := randomString()
		clusterName := randomString()
		clusterType := libsveltosv1beta1.ClusterTypeCapi

		chc := &libsveltosv1beta1.ClusterHealthCheck{
			ObjectMeta: metav1.ObjectMeta{
				Name: randomString(),
			},
			Status: libsveltosv1beta1.ClusterHealthCheckStatus{
				ClusterConditions: []libsveltosv1beta1.ClusterCondition{
					*getClusterCondition(clusterNamespace, clusterName, clusterType),
					*getClusterCondition(clusterNamespace, randomString(), clusterType),
					*getClusterCondition(randomString(), clusterName, clusterType),
					*getClusterCondition(clusterNamespace, clusterName, libsveltosv1beta1.ClusterTypeSveltos),
				},
			},
		}

		initObjects := []client.Object{
			chc,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(initObjects...).
			WithObjects(initObjects...).Build()

		length := len(chc.Status.ClusterConditions)

		Expect(controllers.RemoveConditionEntry(context.TODO(), c, clusterNamespace, clusterName,
			clusterType, chc, logger)).To(Succeed())

		currentChc := &libsveltosv1beta1.ClusterHealthCheck{}
		Expect(c.Get(context.TODO(), types.NamespacedName{Name: chc.Name}, currentChc)).To(Succeed())

		Expect(len(currentChc.Status.ClusterConditions)).To(Equal(length - 1))
	})

	It("updateNotificationSummariesForCluster updates entry for cluster", func() {
		clusterNamespace := randomString()
		clusterName := randomString()
		clusterType := libsveltosv1beta1.ClusterTypeCapi

		cluster := &clusterv1.Cluster{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: clusterNamespace,
				Name:      clusterName,
			},
		}

		chc := &libsveltosv1beta1.ClusterHealthCheck{
			ObjectMeta: metav1.ObjectMeta{
				Name: randomString(),
			},
			Status: libsveltosv1beta1.ClusterHealthCheckStatus{
				ClusterConditions: []libsveltosv1beta1.ClusterCondition{
					*getClusterCondition(clusterNamespace, clusterName, clusterType),
					*getClusterCondition(clusterNamespace, randomString(), clusterType),
					*getClusterCondition(randomString(), clusterName, clusterType),
					*getClusterCondition(clusterNamespace, clusterName, libsveltosv1beta1.ClusterTypeSveltos),
				},
			},
		}

		initObjects := []client.Object{
			chc, cluster,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(initObjects...).
			WithObjects(initObjects...).Build()

		notificationSummary := libsveltosv1beta1.NotificationSummary{
			Name:   randomString(),
			Status: libsveltosv1beta1.NotificationStatusDelivered,
		}

		summaries := []libsveltosv1beta1.NotificationSummary{
			notificationSummary,
		}

		Expect(controllers.UpdateNotificationSummariesForCluster(context.TODO(), c, clusterNamespace, clusterName, clusterType,
			chc, summaries, logger)).To(Succeed())

		currentChc := &libsveltosv1beta1.ClusterHealthCheck{}
		Expect(c.Get(context.TODO(), types.NamespacedName{Name: chc.Name}, currentChc)).To(Succeed())
		Expect(len(currentChc.Status.ClusterConditions)).To(Equal(len(chc.Status.ClusterConditions)))

		var currentNotificationSummaries []libsveltosv1beta1.NotificationSummary
		for i := range currentChc.Status.ClusterConditions {
			cc := &currentChc.Status.ClusterConditions[i]
			if cc.ClusterInfo.Cluster.Namespace == clusterNamespace &&
				cc.ClusterInfo.Cluster.Name == clusterName &&
				cc.ClusterInfo.Cluster.Kind == ClusterKind {
				currentNotificationSummaries = cc.NotificationSummaries
			}
		}

		Expect(currentNotificationSummaries).ToNot((BeNil()))
		Expect(len(currentNotificationSummaries)).To(Equal(1))
		Expect(reflect.DeepEqual(currentNotificationSummaries[0], notificationSummary)).To(BeTrue())
	})

	It("updateConditionsForCluster updates entry for cluster", func() {
		clusterNamespace := randomString()
		clusterName := randomString()
		clusterType := libsveltosv1beta1.ClusterTypeCapi

		cluster := &clusterv1.Cluster{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: clusterNamespace,
				Name:      clusterName,
			},
		}

		chc := &libsveltosv1beta1.ClusterHealthCheck{
			ObjectMeta: metav1.ObjectMeta{
				Name: randomString(),
			},
			Status: libsveltosv1beta1.ClusterHealthCheckStatus{
				ClusterConditions: []libsveltosv1beta1.ClusterCondition{
					*getClusterCondition(clusterNamespace, clusterName, clusterType),
					*getClusterCondition(clusterNamespace, randomString(), clusterType),
					*getClusterCondition(randomString(), clusterName, clusterType),
					*getClusterCondition(clusterNamespace, clusterName, libsveltosv1beta1.ClusterTypeSveltos),
				},
			},
		}

		initObjects := []client.Object{
			chc, cluster,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(initObjects...).
			WithObjects(initObjects...).Build()

		livenessCheck := libsveltosv1beta1.LivenessCheck{
			Name: randomString(),
			Type: libsveltosv1beta1.LivenessTypeAddons,
		}

		conditions := []libsveltosv1beta1.Condition{
			{
				Type:   libsveltosv1beta1.ConditionType(controllers.GetConditionType(&livenessCheck)),
				Status: corev1.ConditionTrue,
			},
		}

		Expect(controllers.UpdateConditionsForCluster(context.TODO(), c, clusterNamespace, clusterName, clusterType,
			chc, conditions, logger)).To(Succeed())

		currentChc := &libsveltosv1beta1.ClusterHealthCheck{}
		Expect(c.Get(context.TODO(), types.NamespacedName{Name: chc.Name}, currentChc)).To(Succeed())
		Expect(len(currentChc.Status.ClusterConditions)).To(Equal(len(chc.Status.ClusterConditions)))

		var currentConditions []libsveltosv1beta1.Condition
		for i := range currentChc.Status.ClusterConditions {
			cc := &currentChc.Status.ClusterConditions[i]
			if cc.ClusterInfo.Cluster.Namespace == clusterNamespace &&
				cc.ClusterInfo.Cluster.Name == clusterName &&
				cc.ClusterInfo.Cluster.Kind == ClusterKind {
				currentConditions = cc.Conditions
			}
		}

		Expect(currentConditions).ToNot((BeNil()))
		Expect(len(currentConditions)).To(Equal(1))
		Expect(reflect.DeepEqual(conditions, currentConditions)).To(BeTrue())
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
					clusterv1.MachineControlPlaneLabel: "ok",
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
			ControllerName:     "classifier",
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

	It("isClusterEntryRemoved returns true when there is no entry for a Cluster in ClusterHealthCheck status", func() {
		clusterNamespace := randomString()
		clusterName := randomString()
		clusterType := libsveltosv1beta1.ClusterTypeCapi

		// Following creates a ClusterSummary and an empty ClusterHealthCheck
		c := prepareClientWithClusterSummaryAndCHC(clusterNamespace, clusterName, clusterType)

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

		// Verify clusterHealthCheck has been created
		chcs := &libsveltosv1beta1.ClusterHealthCheckList{}
		Expect(c.List(context.TODO(), chcs)).To(Succeed())
		Expect(len(chcs.Items)).To(Equal(1))

		chc := chcs.Items[0]

		currentCluster := &clusterv1.Cluster{}
		Expect(c.Get(context.TODO(), types.NamespacedName{Namespace: clusterNamespace, Name: clusterName}, currentCluster)).To(Succeed())
		Expect(addTypeInformationToObject(c.Scheme(), currentCluster)).To(Succeed())

		Expect(controllers.IsClusterEntryRemoved(&reconciler, &chc, controllers.GetKeyFromObject(c.Scheme(), currentCluster))).To(BeTrue())

		chc.Status.ClusterConditions = []libsveltosv1beta1.ClusterCondition{
			{
				ClusterInfo: libsveltosv1beta1.ClusterInfo{
					Cluster: *controllers.GetKeyFromObject(c.Scheme(), currentCluster),
				},
			},
		}
		Expect(c.Status().Update(context.TODO(), &chc)).To(Succeed())

		Expect(controllers.IsClusterEntryRemoved(&reconciler, &chc, controllers.GetKeyFromObject(c.Scheme(), currentCluster))).To(BeFalse())
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
			clusterType, chc, deployer.Options{}, false, logger)).To(Succeed())

		Eventually(func() bool {
			currentHealthCheck := &libsveltosv1beta1.HealthCheck{}
			err := testEnv.Get(context.TODO(), types.NamespacedName{Name: healthCheck.Name}, currentHealthCheck)
			if err != nil {
				return false
			}
			if !util.IsOwnedByObject(currentHealthCheck, chc) {
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
		Expect(testEnv.Client.Update(context.TODO(), healthCheck)).To(Succeed())

		createSecretWithKubeconfig(clusterNamespace, clusterName)

		// Test created HealthCheck instance and added ClusterHealthCheck as ownerReference, indicating healthCheck was deployed
		// because of the ClusterHealthCheck instance.
		// Test has ClusterHealthCheck instance reference a different HealthCheck.
		// RemoveStaleHealthChecks will remove the HealthCheck test created.
		Expect(controllers.RemoveStaleHealthChecks(context.TODO(), testEnv.Client, clusterNamespace, clusterName, clusterType,
			chc, logger)).To(Succeed())

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

func getClusterCondition(clusterNamespace, clusterName string, clusterType libsveltosv1beta1.ClusterType) *libsveltosv1beta1.ClusterCondition {
	var apiVersion, kind string
	if clusterType == libsveltosv1beta1.ClusterTypeCapi {
		apiVersion = clusterv1.GroupVersion.String()
		kind = ClusterKind
	} else {
		apiVersion = libsveltosv1beta1.GroupVersion.String()
		kind = libsveltosv1beta1.SveltosClusterKind
	}

	return &libsveltosv1beta1.ClusterCondition{
		ClusterInfo: libsveltosv1beta1.ClusterInfo{
			Cluster: corev1.ObjectReference{
				Namespace:  clusterNamespace,
				Name:       clusterName,
				Kind:       kind,
				APIVersion: apiVersion,
			},
		},
	}
}
