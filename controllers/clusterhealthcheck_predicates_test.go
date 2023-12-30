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
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2/textlogger"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/event"

	configv1alpha1 "github.com/projectsveltos/addon-controller/api/v1alpha1"
	"github.com/projectsveltos/healthcheck-manager/controllers"
	libsveltosv1alpha1 "github.com/projectsveltos/libsveltos/api/v1alpha1"
)

var _ = Describe("ClusterHealthCheck Predicates: SvelotsClusterPredicates", func() {
	var logger logr.Logger
	var cluster *libsveltosv1alpha1.SveltosCluster
	const upstreamClusterNamePrefix = "sveltoscluster-predicates-"

	BeforeEach(func() {
		logger = textlogger.NewLogger(textlogger.NewConfig(textlogger.Verbosity(1)))
		cluster = &libsveltosv1alpha1.SveltosCluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      upstreamClusterNamePrefix + randomString(),
				Namespace: "predicates" + randomString(),
			},
		}
	})

	It("Create reprocesses when sveltos Cluster is unpaused", func() {
		clusterPredicate := controllers.SveltosClusterPredicates(logger)

		cluster.Spec.Paused = false

		e := event.CreateEvent{
			Object: cluster,
		}

		result := clusterPredicate.Create(e)
		Expect(result).To(BeTrue())
	})
	It("Create does not reprocess when sveltos Cluster is paused", func() {
		clusterPredicate := controllers.SveltosClusterPredicates(logger)

		cluster.Spec.Paused = true
		cluster.Annotations = map[string]string{clusterv1.PausedAnnotation: "true"}

		e := event.CreateEvent{
			Object: cluster,
		}

		result := clusterPredicate.Create(e)
		Expect(result).To(BeFalse())
	})
	It("Delete does reprocess ", func() {
		clusterPredicate := controllers.SveltosClusterPredicates(logger)

		e := event.DeleteEvent{
			Object: cluster,
		}

		result := clusterPredicate.Delete(e)
		Expect(result).To(BeTrue())
	})
	It("Update reprocesses when sveltos Cluster paused changes from true to false", func() {
		clusterPredicate := controllers.SveltosClusterPredicates(logger)

		cluster.Spec.Paused = false

		oldCluster := &libsveltosv1alpha1.SveltosCluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      cluster.Name,
				Namespace: cluster.Namespace,
			},
		}
		oldCluster.Spec.Paused = true
		oldCluster.Annotations = map[string]string{clusterv1.PausedAnnotation: "true"}

		e := event.UpdateEvent{
			ObjectNew: cluster,
			ObjectOld: oldCluster,
		}

		result := clusterPredicate.Update(e)
		Expect(result).To(BeTrue())
	})
	It("Update does not reprocess when sveltos Cluster paused changes from false to true", func() {
		clusterPredicate := controllers.SveltosClusterPredicates(logger)

		cluster.Spec.Paused = true
		cluster.Annotations = map[string]string{clusterv1.PausedAnnotation: "true"}
		oldCluster := &libsveltosv1alpha1.SveltosCluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      cluster.Name,
				Namespace: cluster.Namespace,
			},
		}
		oldCluster.Spec.Paused = false

		e := event.UpdateEvent{
			ObjectNew: cluster,
			ObjectOld: oldCluster,
		}

		result := clusterPredicate.Update(e)
		Expect(result).To(BeFalse())
	})
	It("Update does not reprocess when sveltos Cluster paused has not changed", func() {
		clusterPredicate := controllers.SveltosClusterPredicates(logger)

		cluster.Spec.Paused = false
		oldCluster := &libsveltosv1alpha1.SveltosCluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      cluster.Name,
				Namespace: cluster.Namespace,
			},
		}
		oldCluster.Spec.Paused = false

		e := event.UpdateEvent{
			ObjectNew: cluster,
			ObjectOld: oldCluster,
		}

		result := clusterPredicate.Update(e)
		Expect(result).To(BeFalse())
	})
	It("Update reprocesses when sveltos Cluster labels change", func() {
		clusterPredicate := controllers.SveltosClusterPredicates(logger)

		cluster.Labels = map[string]string{"department": "eng"}

		oldCluster := &libsveltosv1alpha1.SveltosCluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      cluster.Name,
				Namespace: cluster.Namespace,
				Labels:    map[string]string{},
			},
		}

		e := event.UpdateEvent{
			ObjectNew: cluster,
			ObjectOld: oldCluster,
		}

		result := clusterPredicate.Update(e)
		Expect(result).To(BeTrue())
	})
	It("Update reprocesses when sveltos Cluster Status Ready changes", func() {
		clusterPredicate := controllers.SveltosClusterPredicates(logger)

		cluster.Status.Ready = true

		oldCluster := &libsveltosv1alpha1.SveltosCluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      cluster.Name,
				Namespace: cluster.Namespace,
				Labels:    map[string]string{},
			},
			Status: libsveltosv1alpha1.SveltosClusterStatus{
				Ready: false,
			},
		}

		e := event.UpdateEvent{
			ObjectNew: cluster,
			ObjectOld: oldCluster,
		}

		result := clusterPredicate.Update(e)
		Expect(result).To(BeTrue())
	})
})

var _ = Describe("ClusterHealthCheck Predicates: ClusterSummaryPredicates", func() {
	var logger logr.Logger
	var clusterSummary *configv1alpha1.ClusterSummary

	const upstreamClusterNamePrefix = "clustersummary-predicates-"

	BeforeEach(func() {
		logger = textlogger.NewLogger(textlogger.NewConfig(textlogger.Verbosity(1)))
		clusterSummary = &configv1alpha1.ClusterSummary{
			ObjectMeta: metav1.ObjectMeta{
				Name:      upstreamClusterNamePrefix + randomString(),
				Namespace: "predicates" + randomString(),
			},
		}
	})

	It("Create won't reprocesses", func() {
		clusterSummaryPredicate := controllers.ClusterSummaryPredicates(logger)

		e := event.CreateEvent{
			Object: clusterSummary,
		}

		result := clusterSummaryPredicate.Create(e)
		Expect(result).To(BeFalse())
	})
	It("Delete does reprocess ", func() {
		clusterSummaryPredicate := controllers.ClusterSummaryPredicates(logger)

		e := event.DeleteEvent{
			Object: clusterSummary,
		}

		result := clusterSummaryPredicate.Delete(e)
		Expect(result).To(BeTrue())
	})
	It("Update reprocesses when ClusterSummary status FeatureSummaries changes", func() {
		clusterSummaryPredicate := controllers.ClusterSummaryPredicates(logger)

		clusterSummary.Status.FeatureSummaries = []configv1alpha1.FeatureSummary{
			{
				Status:    configv1alpha1.FeatureStatusProvisioned,
				FeatureID: configv1alpha1.FeatureHelm,
			},
		}

		oldClusterSummary := &configv1alpha1.ClusterSummary{
			ObjectMeta: metav1.ObjectMeta{
				Name:      clusterSummary.Name,
				Namespace: clusterSummary.Namespace,
			},
		}

		e := event.UpdateEvent{
			ObjectNew: clusterSummary,
			ObjectOld: oldClusterSummary,
		}

		result := clusterSummaryPredicate.Update(e)
		Expect(result).To(BeTrue())
	})

	It("Update does not reprocesses when ClusterSummary status FeatureSummary has not changed", func() {
		clusterSummaryPredicate := controllers.ClusterSummaryPredicates(logger)

		clusterSummary.Status.FeatureSummaries = []configv1alpha1.FeatureSummary{
			{
				Status:    configv1alpha1.FeatureStatusProvisioned,
				FeatureID: configv1alpha1.FeatureHelm,
			},
		}
		clusterSummary.Status.HelmReleaseSummaries = []configv1alpha1.HelmChartSummary{
			{
				ReleaseName:      randomString(),
				ReleaseNamespace: randomString(),
				Status:           configv1alpha1.HelChartStatusManaging,
			},
		}

		oldClusterSummary := &configv1alpha1.ClusterSummary{
			ObjectMeta: metav1.ObjectMeta{
				Name:      clusterSummary.Name,
				Namespace: clusterSummary.Namespace,
			},
		}

		oldClusterSummary.Status.FeatureSummaries = clusterSummary.Status.FeatureSummaries

		e := event.UpdateEvent{
			ObjectNew: clusterSummary,
			ObjectOld: oldClusterSummary,
		}

		result := clusterSummaryPredicate.Update(e)
		Expect(result).To(BeFalse())
	})
})

var _ = Describe("ClusterHealthCheck Predicates: ClusterPredicates", func() {
	var logger logr.Logger
	var cluster *clusterv1.Cluster

	const upstreamClusterNamePrefix = "cluster-predicates-"

	BeforeEach(func() {
		logger = textlogger.NewLogger(textlogger.NewConfig(textlogger.Verbosity(1)))
		cluster = &clusterv1.Cluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      upstreamClusterNamePrefix + randomString(),
				Namespace: "predicates" + randomString(),
			},
		}
	})

	It("Create reprocesses when v1Cluster is unpaused", func() {
		clusterPredicate := controllers.ClusterPredicates(logger)

		cluster.Spec.Paused = false

		e := event.CreateEvent{
			Object: cluster,
		}

		result := clusterPredicate.Create(e)
		Expect(result).To(BeTrue())
	})
	It("Create does not reprocess when v1Cluster is paused", func() {
		clusterPredicate := controllers.ClusterPredicates(logger)

		cluster.Spec.Paused = true
		cluster.Annotations = map[string]string{clusterv1.PausedAnnotation: "true"}

		e := event.CreateEvent{
			Object: cluster,
		}

		result := clusterPredicate.Create(e)
		Expect(result).To(BeFalse())
	})
	It("Delete does reprocess ", func() {
		clusterPredicate := controllers.ClusterPredicates(logger)

		e := event.DeleteEvent{
			Object: cluster,
		}

		result := clusterPredicate.Delete(e)
		Expect(result).To(BeTrue())
	})
	It("Update reprocesses when v1Cluster paused changes from true to false", func() {
		clusterPredicate := controllers.ClusterPredicates(logger)

		cluster.Spec.Paused = false

		oldCluster := &clusterv1.Cluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      cluster.Name,
				Namespace: cluster.Namespace,
			},
		}
		oldCluster.Spec.Paused = true
		oldCluster.Annotations = map[string]string{clusterv1.PausedAnnotation: "true"}

		e := event.UpdateEvent{
			ObjectNew: cluster,
			ObjectOld: oldCluster,
		}

		result := clusterPredicate.Update(e)
		Expect(result).To(BeTrue())
	})
	It("Update does not reprocess when v1Cluster paused changes from false to true", func() {
		clusterPredicate := controllers.ClusterPredicates(logger)

		cluster.Spec.Paused = true
		cluster.Annotations = map[string]string{clusterv1.PausedAnnotation: "true"}
		oldCluster := &clusterv1.Cluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      cluster.Name,
				Namespace: cluster.Namespace,
			},
		}
		oldCluster.Spec.Paused = false

		e := event.UpdateEvent{
			ObjectNew: cluster,
			ObjectOld: oldCluster,
		}

		result := clusterPredicate.Update(e)
		Expect(result).To(BeFalse())
	})
	It("Update does not reprocess when v1Cluster paused has not changed", func() {
		clusterPredicate := controllers.ClusterPredicates(logger)

		cluster.Spec.Paused = false
		oldCluster := &clusterv1.Cluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      cluster.Name,
				Namespace: cluster.Namespace,
			},
		}
		oldCluster.Spec.Paused = false

		e := event.UpdateEvent{
			ObjectNew: cluster,
			ObjectOld: oldCluster,
		}

		result := clusterPredicate.Update(e)
		Expect(result).To(BeFalse())
	})
	It("Update reprocesses when v1Cluster labels change", func() {
		clusterPredicate := controllers.ClusterPredicates(logger)

		cluster.Labels = map[string]string{"department": "eng"}

		oldCluster := &clusterv1.Cluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      cluster.Name,
				Namespace: cluster.Namespace,
				Labels:    map[string]string{},
			},
		}

		e := event.UpdateEvent{
			ObjectNew: cluster,
			ObjectOld: oldCluster,
		}

		result := clusterPredicate.Update(e)
		Expect(result).To(BeTrue())
	})
})

var _ = Describe("ClusterHealthCheck Predicates: MachinePredicates", func() {
	var logger logr.Logger
	var machine *clusterv1.Machine

	const upstreamMachineNamePrefix = "machine-predicates-"

	BeforeEach(func() {
		logger = textlogger.NewLogger(textlogger.NewConfig(textlogger.Verbosity(1)))
		machine = &clusterv1.Machine{
			ObjectMeta: metav1.ObjectMeta{
				Name:      upstreamMachineNamePrefix + randomString(),
				Namespace: "predicates" + randomString(),
			},
		}
	})

	It("Create reprocesses when v1Machine is Running", func() {
		machinePredicate := controllers.MachinePredicates(logger)

		machine.Status.Phase = string(clusterv1.MachinePhaseRunning)

		e := event.CreateEvent{
			Object: machine,
		}

		result := machinePredicate.Create(e)
		Expect(result).To(BeTrue())
	})
	It("Create does not reprocess when v1Machine is not Running", func() {
		machinePredicate := controllers.MachinePredicates(logger)

		e := event.CreateEvent{
			Object: machine,
		}

		result := machinePredicate.Create(e)
		Expect(result).To(BeFalse())
	})
	It("Delete does not reprocess ", func() {
		machinePredicate := controllers.MachinePredicates(logger)

		e := event.DeleteEvent{
			Object: machine,
		}

		result := machinePredicate.Delete(e)
		Expect(result).To(BeFalse())
	})
	It("Update reprocesses when v1Machine Phase changed from not running to running", func() {
		machinePredicate := controllers.MachinePredicates(logger)

		machine.Status.Phase = string(clusterv1.MachinePhaseRunning)

		oldMachine := &clusterv1.Machine{
			ObjectMeta: metav1.ObjectMeta{
				Name:      machine.Name,
				Namespace: machine.Namespace,
			},
		}

		e := event.UpdateEvent{
			ObjectNew: machine,
			ObjectOld: oldMachine,
		}

		result := machinePredicate.Update(e)
		Expect(result).To(BeTrue())
	})
	It("Update does not reprocess when v1Machine Phase changes from not Phase not set to Phase set but not running", func() {
		machinePredicate := controllers.MachinePredicates(logger)

		machine.Status.Phase = "Provisioning"

		oldMachine := &clusterv1.Machine{
			ObjectMeta: metav1.ObjectMeta{
				Name:      machine.Name,
				Namespace: machine.Namespace,
			},
		}

		e := event.UpdateEvent{
			ObjectNew: machine,
			ObjectOld: oldMachine,
		}

		result := machinePredicate.Update(e)
		Expect(result).To(BeFalse())
	})
	It("Update does not reprocess when v1Machine Phases does not change", func() {
		machinePredicate := controllers.MachinePredicates(logger)
		machine.Status.Phase = string(clusterv1.MachinePhaseRunning)

		oldMachine := &clusterv1.Machine{
			ObjectMeta: metav1.ObjectMeta{
				Name:      machine.Name,
				Namespace: machine.Namespace,
			},
		}
		oldMachine.Status.Phase = machine.Status.Phase

		e := event.UpdateEvent{
			ObjectNew: machine,
			ObjectOld: oldMachine,
		}

		result := machinePredicate.Update(e)
		Expect(result).To(BeFalse())
	})
})

var _ = Describe("ClusterHealthCheck Predicates: HealthCheckReportPredicates", func() {
	var logger logr.Logger
	var healthCheckReport *libsveltosv1alpha1.HealthCheckReport

	const upstreamClusterNamePrefix = "healthcheckreport-predicates-"

	BeforeEach(func() {
		logger = textlogger.NewLogger(textlogger.NewConfig(textlogger.Verbosity(1)))
		healthCheckReport = &libsveltosv1alpha1.HealthCheckReport{
			ObjectMeta: metav1.ObjectMeta{
				Name:      upstreamClusterNamePrefix + randomString(),
				Namespace: "predicates" + randomString(),
			},
		}
	})

	It("Create will reprocesses", func() {
		hcrPredicate := controllers.HealthCheckReportPredicates(logger)

		e := event.CreateEvent{
			Object: healthCheckReport,
		}

		result := hcrPredicate.Create(e)
		Expect(result).To(BeTrue())
	})
	It("Delete does reprocess ", func() {
		hcrPredicate := controllers.HealthCheckReportPredicates(logger)

		e := event.DeleteEvent{
			Object: healthCheckReport,
		}

		result := hcrPredicate.Delete(e)
		Expect(result).To(BeTrue())
	})
	It("Update reprocesses when HealthCheckReport spec changes", func() {
		hcrPredicate := controllers.HealthCheckReportPredicates(logger)

		healthCheckReport.Spec = libsveltosv1alpha1.HealthCheckReportSpec{
			ResourceStatuses: []libsveltosv1alpha1.ResourceStatus{
				{
					ObjectRef: corev1.ObjectReference{
						Kind:       randomString(),
						APIVersion: randomString(),
						Name:       randomString(),
						Namespace:  randomString(),
					},
				},
			},
		}

		oldHealthCheckReport := &libsveltosv1alpha1.HealthCheckReport{
			ObjectMeta: metav1.ObjectMeta{
				Name:      healthCheckReport.Name,
				Namespace: healthCheckReport.Namespace,
			},
		}

		e := event.UpdateEvent{
			ObjectNew: healthCheckReport,
			ObjectOld: oldHealthCheckReport,
		}

		result := hcrPredicate.Update(e)
		Expect(result).To(BeTrue())
	})

	It("Update does not reprocesses HealthCheckReport spec has not changed", func() {
		hcrPredicate := controllers.HealthCheckReportPredicates(logger)

		healthCheckReport.Spec = libsveltosv1alpha1.HealthCheckReportSpec{
			ResourceStatuses: []libsveltosv1alpha1.ResourceStatus{
				{
					ObjectRef: corev1.ObjectReference{
						Kind:       randomString(),
						APIVersion: randomString(),
						Name:       randomString(),
						Namespace:  randomString(),
					},
				},
			},
		}

		oldHealthCheckReport := &libsveltosv1alpha1.HealthCheckReport{
			ObjectMeta: metav1.ObjectMeta{
				Name:      healthCheckReport.Name,
				Namespace: healthCheckReport.Namespace,
			},
			Spec: healthCheckReport.Spec,
		}

		e := event.UpdateEvent{
			ObjectNew: healthCheckReport,
			ObjectOld: oldHealthCheckReport,
		}

		result := hcrPredicate.Update(e)
		Expect(result).To(BeFalse())
	})
})

var _ = Describe("ClusterHealthCheck Predicates: HealthCheckPredicates", func() {
	var logger logr.Logger
	var healthCheck *libsveltosv1alpha1.HealthCheck

	const upstreamClusterNamePrefix = "healthcheck-predicates-"

	BeforeEach(func() {
		logger = textlogger.NewLogger(textlogger.NewConfig(textlogger.Verbosity(1)))
		healthCheck = &libsveltosv1alpha1.HealthCheck{
			ObjectMeta: metav1.ObjectMeta{
				Name: upstreamClusterNamePrefix + randomString(),
			},
		}
	})

	It("Create will reprocesses", func() {
		hcrPredicate := controllers.HealthCheckPredicates(logger)

		e := event.CreateEvent{
			Object: healthCheck,
		}

		result := hcrPredicate.Create(e)
		Expect(result).To(BeTrue())
	})
	It("Delete does reprocess ", func() {
		hcrPredicate := controllers.HealthCheckPredicates(logger)

		e := event.DeleteEvent{
			Object: healthCheck,
		}

		result := hcrPredicate.Delete(e)
		Expect(result).To(BeTrue())
	})
	It("Update reprocesses when HealthCheck spec changes", func() {
		hcrPredicate := controllers.HealthCheckPredicates(logger)

		healthCheck.Spec = libsveltosv1alpha1.HealthCheckSpec{
			Group:   randomString(),
			Version: randomString(),
			Kind:    randomString(),
			Script:  randomString(),
		}

		oldHealthCheck := &libsveltosv1alpha1.HealthCheck{
			ObjectMeta: metav1.ObjectMeta{
				Name: healthCheck.Name,
			},
		}

		e := event.UpdateEvent{
			ObjectNew: healthCheck,
			ObjectOld: oldHealthCheck,
		}

		result := hcrPredicate.Update(e)
		Expect(result).To(BeTrue())
	})

	It("Update does not reprocesses HealthCheck spec has not changed", func() {
		hcrPredicate := controllers.HealthCheckPredicates(logger)

		healthCheck.Spec = libsveltosv1alpha1.HealthCheckSpec{
			Group:   randomString(),
			Version: randomString(),
			Kind:    randomString(),
			Script:  randomString(),
		}

		oldHealthCheck := &libsveltosv1alpha1.HealthCheck{
			ObjectMeta: metav1.ObjectMeta{
				Name: healthCheck.Name,
			},
			Spec: healthCheck.Spec,
		}

		e := event.UpdateEvent{
			ObjectNew: healthCheck,
			ObjectOld: oldHealthCheck,
		}

		result := hcrPredicate.Update(e)
		Expect(result).To(BeFalse())
	})
})
