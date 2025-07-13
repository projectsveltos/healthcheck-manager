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
	"sigs.k8s.io/controller-runtime/pkg/event"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	"github.com/projectsveltos/healthcheck-manager/controllers"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
)

const (
	predicates = "predicates"
)

var _ = Describe("ClusterHealthCheck Predicates: ClusterSummaryPredicates", func() {
	var logger logr.Logger
	var clusterSummary *configv1beta1.ClusterSummary

	const upstreamClusterNamePrefix = "clustersummary-predicates-"

	BeforeEach(func() {
		logger = textlogger.NewLogger(textlogger.NewConfig(textlogger.Verbosity(1)))
		clusterSummary = &configv1beta1.ClusterSummary{
			ObjectMeta: metav1.ObjectMeta{
				Name:      upstreamClusterNamePrefix + randomString(),
				Namespace: predicates + randomString(),
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

		clusterSummary.Status.FeatureSummaries = []configv1beta1.FeatureSummary{
			{
				Status:    libsveltosv1beta1.FeatureStatusProvisioned,
				FeatureID: libsveltosv1beta1.FeatureHelm,
			},
		}

		oldClusterSummary := &configv1beta1.ClusterSummary{
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

		clusterSummary.Status.FeatureSummaries = []configv1beta1.FeatureSummary{
			{
				Status:    libsveltosv1beta1.FeatureStatusProvisioned,
				FeatureID: libsveltosv1beta1.FeatureHelm,
			},
		}
		clusterSummary.Status.HelmReleaseSummaries = []configv1beta1.HelmChartSummary{
			{
				ReleaseName:      randomString(),
				ReleaseNamespace: randomString(),
				Status:           configv1beta1.HelmChartStatusManaging,
			},
		}

		oldClusterSummary := &configv1beta1.ClusterSummary{
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

var _ = Describe("ClusterHealthCheck Predicates: HealthCheckReportPredicates", func() {
	var logger logr.Logger
	var healthCheckReport *libsveltosv1beta1.HealthCheckReport

	const upstreamClusterNamePrefix = "healthcheckreport-predicates-"

	BeforeEach(func() {
		logger = textlogger.NewLogger(textlogger.NewConfig(textlogger.Verbosity(1)))
		healthCheckReport = &libsveltosv1beta1.HealthCheckReport{
			ObjectMeta: metav1.ObjectMeta{
				Name:      upstreamClusterNamePrefix + randomString(),
				Namespace: predicates + randomString(),
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

		healthCheckReport.Spec = libsveltosv1beta1.HealthCheckReportSpec{
			ResourceStatuses: []libsveltosv1beta1.ResourceStatus{
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

		oldHealthCheckReport := &libsveltosv1beta1.HealthCheckReport{
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

		healthCheckReport.Spec = libsveltosv1beta1.HealthCheckReportSpec{
			ResourceStatuses: []libsveltosv1beta1.ResourceStatus{
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

		oldHealthCheckReport := &libsveltosv1beta1.HealthCheckReport{
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
	var healthCheck *libsveltosv1beta1.HealthCheck

	const upstreamClusterNamePrefix = "healthcheck-predicates-"

	BeforeEach(func() {
		logger = textlogger.NewLogger(textlogger.NewConfig(textlogger.Verbosity(1)))
		healthCheck = &libsveltosv1beta1.HealthCheck{
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

		healthCheck.Spec = libsveltosv1beta1.HealthCheckSpec{
			ResourceSelectors: []libsveltosv1beta1.ResourceSelector{
				{
					Group:    randomString(),
					Version:  randomString(),
					Kind:     randomString(),
					Evaluate: randomString(),
				},
			},
		}

		oldHealthCheck := &libsveltosv1beta1.HealthCheck{
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

		healthCheck.Spec = libsveltosv1beta1.HealthCheckSpec{
			ResourceSelectors: []libsveltosv1beta1.ResourceSelector{
				{
					Group:    randomString(),
					Version:  randomString(),
					Kind:     randomString(),
					Evaluate: randomString(),
				},
			},
		}

		oldHealthCheck := &libsveltosv1beta1.HealthCheck{
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
