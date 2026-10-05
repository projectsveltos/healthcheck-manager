/*
Copyright 2026. projectsveltos.io. All rights reserved.

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
	"fmt"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/klog/v2/textlogger"
	"sigs.k8s.io/controller-runtime/pkg/metrics"

	"github.com/projectsveltos/healthcheck-manager/controllers"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
)

// clusterMetricFamilyName returns the name of the per cluster histogram.
// It mirrors how the metric is named in newClusterHealthCheckHistogram.
func clusterMetricFamilyName(clusterNamespace, clusterName string,
	clusterType libsveltosv1beta1.ClusterType) string {

	clusterInfo := strings.ReplaceAll(fmt.Sprintf("%s_%s_%s", clusterType, clusterNamespace, clusterName), "-", "_")
	return clusterInfo + "_program_clusterHealthCheck_time_seconds"
}

// clusterHistogramSampleCount returns how many observations the per cluster histogram holds.
func clusterHistogramSampleCount(clusterNamespace, clusterName string,
	clusterType libsveltosv1beta1.ClusterType) uint64 {

	metricFamilies, err := metrics.Registry.Gather()
	Expect(err).To(BeNil())

	name := clusterMetricFamilyName(clusterNamespace, clusterName, clusterType)
	for _, family := range metricFamilies {
		if family.GetName() == name {
			Expect(family.GetMetric()).To(HaveLen(1))
			return family.GetMetric()[0].GetHistogram().GetSampleCount()
		}
	}

	return 0
}

var _ = Describe("Metrics", func() {
	var clusterNamespace, clusterName string
	var clusterType libsveltosv1beta1.ClusterType

	BeforeEach(func() {
		clusterNamespace = randomString()
		clusterName = randomString()
		clusterType = libsveltosv1beta1.ClusterTypeCapi
	})

	It("newClusterHealthCheckHistogram returns the registered histogram when called again for same cluster", func() {
		logger := textlogger.NewLogger(textlogger.NewConfig())

		first := controllers.NewClusterHealthCheckHistogram(clusterNamespace, clusterName, clusterType, logger)
		Expect(first).ToNot(BeNil())

		second := controllers.NewClusterHealthCheckHistogram(clusterNamespace, clusterName, clusterType, logger)
		Expect(second).ToNot(BeNil())
		Expect(second).To(BeIdenticalTo(first))
	})

	It("newClusterHealthCheckHistogram returns different histograms for different clusters", func() {
		logger := textlogger.NewLogger(textlogger.NewConfig())

		first := controllers.NewClusterHealthCheckHistogram(clusterNamespace, clusterName, clusterType, logger)
		Expect(first).ToNot(BeNil())

		second := controllers.NewClusterHealthCheckHistogram(clusterNamespace, randomString(), clusterType, logger)
		Expect(second).ToNot(BeNil())
		Expect(second).ToNot(BeIdenticalTo(first))
	})

	It("programDuration records every observation in the per cluster histogram", func() {
		logger := textlogger.NewLogger(textlogger.NewConfig())
		featureID := string(libsveltosv1beta1.FeatureClusterHealthCheck)

		const observations = 3
		for i := 0; i < observations; i++ {
			controllers.ProgramDuration(time.Second, clusterNamespace, clusterName, featureID, clusterType, logger)
		}

		Expect(clusterHistogramSampleCount(clusterNamespace, clusterName, clusterType)).To(Equal(uint64(observations)))
	})

	It("programDuration ignores features other than ClusterHealthCheck", func() {
		logger := textlogger.NewLogger(textlogger.NewConfig())

		controllers.ProgramDuration(time.Second, clusterNamespace, clusterName, "NotClusterHealthCheck", clusterType, logger)

		Expect(clusterHistogramSampleCount(clusterNamespace, clusterName, clusterType)).To(Equal(uint64(0)))
	})
})
