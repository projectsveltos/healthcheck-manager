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
	"fmt"
	"os"
	"runtime"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gmeasure"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"

	"github.com/projectsveltos/healthcheck-manager/controllers"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
)

const runBenchmarksEnv = "RUN_BENCHMARKS"

// Run with: RUN_BENCHMARKS=1 make test TEST_ARGS="-ginkgo.focus 'at scale' -ginkgo.v"
var _ = Describe("HealthCheckReport collection at scale", Label("BENCHMARK"), func() {
	BeforeEach(func() {
		if os.Getenv(runBenchmarksEnv) == "" {
			Skip(fmt.Sprintf("set %s to run benchmarks", runBenchmarksEnv))
		}
	})

	type scenario struct {
		name                 string
		clusters             int
		shardClusters        int
		clusterHealthChecks  int
		healthChecksPerCHC   int
		matchingClustersPerC int
	}

	scenarios := []scenario{
		{"typical: 1000 clusters, each matched by 100 ClusterHealthChecks, 2 HealthChecks each", 1000, 1000, 100, 2, 1000},
		{"typical: 1000 clusters, each matched by 100 ClusterHealthChecks, 1 HealthCheck each", 1000, 1000, 100, 1, 1000},
		{"every ClusterHealthCheck matches the whole fleet (worst case)", 10000, 10000, 50, 2, 10000},
		{"every ClusterHealthCheck matches 10% of the fleet", 10000, 10000, 50, 2, 1000},
		{"worst case, shard owns 1000 of 10000 clusters", 10000, 1000, 50, 2, 10000},
	}

	for i := range scenarios {
		s := scenarios[i]
		It(s.name, func() {
			clusters := make([]corev1.ObjectReference, s.clusters)
			for j := range clusters {
				clusters[j] = corev1.ObjectReference{Namespace: "ns", Name: fmt.Sprintf("cluster-%d", j), Kind: ClusterKind,
					APIVersion: clusterv1.GroupVersion.String()}
			}
			shard := clusters[:s.shardClusters]

			list := &libsveltosv1beta1.ClusterHealthCheckList{}
			for j := 0; j < s.clusterHealthChecks; j++ {
				chc := libsveltosv1beta1.ClusterHealthCheck{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("chc-%d", j)}}
				for k := 0; k < s.healthChecksPerCHC; k++ {
					chc.Spec.LivenessChecks = append(chc.Spec.LivenessChecks, libsveltosv1beta1.LivenessCheck{
						Name: fmt.Sprintf("lc-%d", k),
						Type: libsveltosv1beta1.LivenessTypeHealthCheck,
						LivenessSourceRef: &corev1.ObjectReference{
							Kind: libsveltosv1beta1.HealthCheckKind,
							Name: fmt.Sprintf("hc-%d-%d", j, k),
						},
					})
				}
				chc.Status.MatchingClusterRefs = clusters[:s.matchingClustersPerC]
				list.Items = append(list.Items, chc)
			}

			experiment := gmeasure.NewExperiment("collection filter")

			experiment.Sample(func(_ int) {
				experiment.MeasureDuration("buildClustersWithHealthCheck (existing)", func() {
					controllers.BuildClustersWithHealthCheck(list)
				})
				experiment.MeasureDuration("buildWantedReports (new)", func() {
					controllers.BuildWantedReports(list, shard)
				})
			}, gmeasure.SamplingConfig{N: 10})

			AddReportEntry(experiment.Name, experiment)

			// Allocations of one call to each builder
			before := allocations()
			controllers.BuildClustersWithHealthCheck(list)
			existing := allocations().since(before)

			before = allocations()
			wanted := controllers.BuildWantedReports(list, shard)
			added := allocations().since(before)

			AddReportEntry("allocations", fmt.Sprintf(
				"buildClustersWithHealthCheck: %d allocs, %.1f MiB\nbuildWantedReports: %d allocs, %.1f MiB, %d entries",
				existing.mallocs, mib(existing.bytes), added.mallocs, mib(added.bytes), len(wanted)))

			Expect(wanted).ToNot(BeEmpty())
			Expect(experiment.GetStats("buildWantedReports (new)").DurationFor(gmeasure.StatMedian)).To(BeNumerically(">", time.Duration(0)))
		})
	}
})

type allocationCounters struct {
	mallocs uint64
	bytes   uint64
}

func allocations() allocationCounters {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return allocationCounters{mallocs: m.Mallocs, bytes: m.TotalAlloc}
}

func (a allocationCounters) since(before allocationCounters) allocationCounters {
	return allocationCounters{mallocs: a.mallocs - before.mallocs, bytes: a.bytes - before.bytes}
}

func mib(bytes uint64) float64 {
	return float64(bytes) / (1024 * 1024)
}
