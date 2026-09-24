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

// Package healthchecks_test exercises the example HealthCheck manifests in this
// directory against fixture resources, so the Lua shipped in the docs is verified
// to classify Healthy/Progressing/Degraded cases the way the docs claim.
//
// This does not reuse sveltos-agent's own Lua evaluation pipeline (pooling, chunking,
// the empty-resources edge case are all sveltos-agent internals, and that repo is
// private). Instead it runs the evaluateHealth script directly against gopher-lua,
// using the same module preloading (libsveltos/lib/lua) production relies on, which
// is enough to verify script correctness against the documented evaluate() contract.
package healthchecks_test

import (
	"fmt"
	"os"
	"testing"

	lua "github.com/yuin/gopher-lua"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/yaml"

	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
	sveltoslua "github.com/projectsveltos/libsveltos/lib/lua"
)

const (
	phaseKey   = "phase"
	errorKey   = "error"
	statusKey  = "status"
	failKey    = "fail"
	passKey    = "pass"
	summaryKey = "summary"

	conditionsKey      = "conditions"
	typeKey            = "type"
	reasonKey          = "reason"
	specKey            = "spec"
	replicasKey        = "replicas"
	readyReplicasKey   = "readyReplicas"
	currentRevisionKey = "currentRevision"
	updateRevisionKey  = "updateRevision"

	statefulSetRevision = "postgres-abc"
)

func TestHealthchecks(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Example HealthCheck library Suite")
}

// resourceResult mirrors one entry of the evaluate() function's returned
// hs.resources array (see HealthCheckSpec.EvaluateHealth godoc in libsveltos).
type resourceResult struct {
	Status  string
	Message string
}

// loadEvaluateHealth reads an example HealthCheck manifest from this directory
// and returns its spec.evaluateHealth script.
func loadEvaluateHealth(fileName string) string {
	content, err := os.ReadFile(fileName)
	Expect(err).To(BeNil())

	healthCheck := &libsveltosv1beta1.HealthCheck{}
	Expect(yaml.Unmarshal(content, healthCheck)).To(Succeed())
	Expect(healthCheck.Spec.EvaluateHealth).ToNot(BeEmpty())

	return healthCheck.Spec.EvaluateHealth
}

// runEvaluateHealth runs an evaluateHealth script against a set of resources the
// same way a HealthCheck's evaluate() function is invoked: resources exposed as
// the global resources table, evaluate() called, result converted back to Go.
func runEvaluateHealth(script string, resources []*unstructured.Unstructured) (topStatus, topMessage string,
	results []resourceResult) {

	l := lua.NewState()
	defer l.Close()

	sveltoslua.LoadModulesAndRegisterMethods(l)

	resourcesTable := &lua.LTable{}
	for _, resource := range resources {
		resourcesTable.Append(sveltoslua.MapToTable(resource.UnstructuredContent()))
	}
	l.SetGlobal("resources", resourcesTable)

	Expect(l.DoString(script)).To(Succeed())

	Expect(l.CallByParam(lua.P{
		Fn:      l.GetGlobal("evaluate"),
		NRet:    1,
		Protect: true,
	})).To(Succeed())

	ret := l.Get(-1)
	l.Pop(1)

	table, ok := ret.(*lua.LTable)
	Expect(ok).To(BeTrue(), "evaluate() must return a table")

	goValue, ok := sveltoslua.ToGoValue(table).(map[string]any)
	Expect(ok).To(BeTrue())

	if status, ok := goValue["status"].(string); ok {
		topStatus = status
	}
	if message, ok := goValue["message"].(string); ok {
		topMessage = message
	}

	rawResources, ok := goValue["resources"]
	if !ok {
		return topStatus, topMessage, nil
	}

	list, ok := rawResources.([]any)
	Expect(ok).To(BeTrue())

	for _, item := range list {
		entry, ok := item.(map[string]any)
		Expect(ok).To(BeTrue())

		result := resourceResult{}
		if status, ok := entry["status"].(string); ok {
			result.Status = status
		}
		if message, ok := entry["message"].(string); ok {
			result.Message = message
		}
		results = append(results, result)
	}

	return topStatus, topMessage, results
}

func toUnstructured(apiVersion, kind, namespace, name string, extra map[string]any) *unstructured.Unstructured {
	object := map[string]any{
		"apiVersion": apiVersion,
		"kind":       kind,
		"metadata": map[string]any{
			"namespace": namespace,
			"name":      name,
		},
	}
	for k, v := range extra {
		object[k] = v
	}
	return &unstructured.Unstructured{Object: object}
}

// condition builds one status.conditions[] entry as used by the Kubernetes/Knative
// "duck type" condition convention (cert-manager, Job, HTTPProxy, Knative Service).
func condition(condType, condStatus, reason string) map[string]any {
	return map[string]any{
		typeKey:   condType,
		statusKey: condStatus,
		reasonKey: reason,
	}
}

var _ = Describe("velero-backup.yaml", func() {
	const scriptFile = "velero-backup.yaml"

	It("reports Healthy when the Backup completed", func() {
		script := loadEvaluateHealth(scriptFile)

		backup := toUnstructured("velero.io/v1", "Backup", "velero", "nightly-backup", map[string]any{
			statusKey: map[string]any{phaseKey: "Completed"},
		})

		_, _, results := runEvaluateHealth(script, []*unstructured.Unstructured{backup})
		Expect(results).To(BeEmpty())
	})

	It("reports Degraded when the Backup failed", func() {
		script := loadEvaluateHealth(scriptFile)

		backup := toUnstructured("velero.io/v1", "Backup", "velero", "nightly-backup", map[string]any{
			statusKey: map[string]any{phaseKey: "Failed"},
		})

		_, _, results := runEvaluateHealth(script, []*unstructured.Unstructured{backup})
		Expect(results).To(HaveLen(1))
		Expect(results[0].Status).To(Equal(string(libsveltosv1beta1.HealthStatusDegraded)))
		Expect(results[0].Message).To(Equal("Backup velero/nightly-backup is Failed"))
	})

	It("reports Progressing while the Backup is still running", func() {
		script := loadEvaluateHealth(scriptFile)

		backup := toUnstructured("velero.io/v1", "Backup", "velero", "nightly-backup", map[string]any{
			statusKey: map[string]any{phaseKey: "InProgress"},
		})

		_, _, results := runEvaluateHealth(script, []*unstructured.Unstructured{backup})
		Expect(results).To(HaveLen(1))
		Expect(results[0].Status).To(Equal(string(libsveltosv1beta1.HealthStatusProgressing)))
	})

	It("reports a top-level Healthy status when no Backup matched", func() {
		script := loadEvaluateHealth(scriptFile)

		topStatus, topMessage, results := runEvaluateHealth(script, []*unstructured.Unstructured{})
		Expect(results).To(BeEmpty())
		Expect(topStatus).To(Equal(string(libsveltosv1beta1.HealthStatusHealthy)))
		Expect(topMessage).To(BeEmpty())
	})
})

var _ = Describe("kyverno-policyreport.yaml", func() {
	const scriptFile = "kyverno-policyreport.yaml"

	It("reports Healthy when no rule failed or errored", func() {
		script := loadEvaluateHealth(scriptFile)

		report := toUnstructured("wgpolicyk8s.io/v1alpha2", "PolicyReport", "team-a", "polr-team-a", map[string]any{
			summaryKey: map[string]any{passKey: float64(5), failKey: float64(0), errorKey: float64(0)},
		})

		_, _, results := runEvaluateHealth(script, []*unstructured.Unstructured{report})
		Expect(results).To(BeEmpty())
	})

	It("reports Degraded when the summary has failing rules", func() {
		script := loadEvaluateHealth(scriptFile)

		report := toUnstructured("wgpolicyk8s.io/v1alpha2", "PolicyReport", "team-a", "polr-team-a", map[string]any{
			summaryKey: map[string]any{passKey: float64(3), failKey: float64(2), errorKey: float64(0)},
		})

		_, _, results := runEvaluateHealth(script, []*unstructured.Unstructured{report})
		Expect(results).To(HaveLen(1))
		Expect(results[0].Status).To(Equal(string(libsveltosv1beta1.HealthStatusDegraded)))
		Expect(results[0].Message).To(Equal(
			fmt.Sprintf("PolicyReport team-a/polr-team-a has %d failing and %d erroring rule(s)", 2, 0)))
	})

	It("reports Degraded when the summary has erroring rules", func() {
		script := loadEvaluateHealth(scriptFile)

		report := toUnstructured("wgpolicyk8s.io/v1alpha2", "PolicyReport", "team-a", "polr-team-a", map[string]any{
			summaryKey: map[string]any{passKey: float64(3), failKey: float64(0), errorKey: float64(1)},
		})

		_, _, results := runEvaluateHealth(script, []*unstructured.Unstructured{report})
		Expect(results).To(HaveLen(1))
		Expect(results[0].Status).To(Equal(string(libsveltosv1beta1.HealthStatusDegraded)))
	})

	It("reports a top-level Healthy status when no PolicyReport matched", func() {
		script := loadEvaluateHealth(scriptFile)

		topStatus, _, results := runEvaluateHealth(script, []*unstructured.Unstructured{})
		Expect(results).To(BeEmpty())
		Expect(topStatus).To(Equal(string(libsveltosv1beta1.HealthStatusHealthy)))
	})
})

var _ = Describe("certmanager-certificate.yaml", func() {
	const scriptFile = "certmanager-certificate.yaml"

	It("reports Healthy when the Ready condition is True", func() {
		script := loadEvaluateHealth(scriptFile)

		cert := toUnstructured("cert-manager.io/v1", "Certificate", "web", "web-tls", map[string]any{
			statusKey: map[string]any{
				conditionsKey: []any{condition("Ready", "True", "Ready")},
			},
		})

		_, _, results := runEvaluateHealth(script, []*unstructured.Unstructured{cert})
		Expect(results).To(BeEmpty())
	})

	It("reports Degraded when the Ready condition is False", func() {
		script := loadEvaluateHealth(scriptFile)

		cert := toUnstructured("cert-manager.io/v1", "Certificate", "web", "web-tls", map[string]any{
			statusKey: map[string]any{
				conditionsKey: []any{condition("Ready", "False", "Failed")},
			},
		})

		_, _, results := runEvaluateHealth(script, []*unstructured.Unstructured{cert})
		Expect(results).To(HaveLen(1))
		Expect(results[0].Status).To(Equal(string(libsveltosv1beta1.HealthStatusDegraded)))
		Expect(results[0].Message).To(Equal("Certificate web/web-tls not Ready (Failed)"))
	})

	It("reports Degraded when there is no Ready condition at all", func() {
		script := loadEvaluateHealth(scriptFile)

		cert := toUnstructured("cert-manager.io/v1", "Certificate", "web", "web-tls", map[string]any{})

		_, _, results := runEvaluateHealth(script, []*unstructured.Unstructured{cert})
		Expect(results).To(HaveLen(1))
		Expect(results[0].Status).To(Equal(string(libsveltosv1beta1.HealthStatusDegraded)))
	})

	It("reports a top-level Healthy status when no Certificate matched", func() {
		script := loadEvaluateHealth(scriptFile)

		topStatus, _, results := runEvaluateHealth(script, []*unstructured.Unstructured{})
		Expect(results).To(BeEmpty())
		Expect(topStatus).To(Equal(string(libsveltosv1beta1.HealthStatusHealthy)))
	})
})

var _ = Describe("batch-job.yaml", func() {
	const scriptFile = "batch-job.yaml"

	It("reports Healthy when the Job has completed", func() {
		script := loadEvaluateHealth(scriptFile)

		job := toUnstructured("batch/v1", "Job", "batch", "nightly-import", map[string]any{
			statusKey: map[string]any{
				conditionsKey: []any{condition("Complete", "True", "")},
			},
		})

		_, _, results := runEvaluateHealth(script, []*unstructured.Unstructured{job})
		Expect(results).To(BeEmpty())
	})

	It("reports Degraded when the Job failed", func() {
		script := loadEvaluateHealth(scriptFile)

		job := toUnstructured("batch/v1", "Job", "batch", "nightly-import", map[string]any{
			statusKey: map[string]any{
				conditionsKey: []any{condition("Failed", "True", "BackoffLimitExceeded")},
			},
		})

		_, _, results := runEvaluateHealth(script, []*unstructured.Unstructured{job})
		Expect(results).To(HaveLen(1))
		Expect(results[0].Status).To(Equal(string(libsveltosv1beta1.HealthStatusDegraded)))
		Expect(results[0].Message).To(Equal("Job batch/nightly-import failed: BackoffLimitExceeded"))
	})

	It("reports Progressing while the Job is still running", func() {
		script := loadEvaluateHealth(scriptFile)

		job := toUnstructured("batch/v1", "Job", "batch", "nightly-import", map[string]any{
			statusKey: map[string]any{},
		})

		_, _, results := runEvaluateHealth(script, []*unstructured.Unstructured{job})
		Expect(results).To(HaveLen(1))
		Expect(results[0].Status).To(Equal(string(libsveltosv1beta1.HealthStatusProgressing)))
	})

	It("reports a top-level Healthy status when no Job matched", func() {
		script := loadEvaluateHealth(scriptFile)

		topStatus, _, results := runEvaluateHealth(script, []*unstructured.Unstructured{})
		Expect(results).To(BeEmpty())
		Expect(topStatus).To(Equal(string(libsveltosv1beta1.HealthStatusHealthy)))
	})
})

var _ = Describe("statefulset-rollout.yaml", func() {
	const scriptFile = "statefulset-rollout.yaml"

	It("reports Healthy when fully rolled out and ready", func() {
		script := loadEvaluateHealth(scriptFile)

		sts := toUnstructured("apps/v1", "StatefulSet", "db", "postgres", map[string]any{
			specKey: map[string]any{replicasKey: float64(3)},
			statusKey: map[string]any{
				readyReplicasKey:   float64(3),
				currentRevisionKey: statefulSetRevision,
				updateRevisionKey:  statefulSetRevision,
			},
		})

		_, _, results := runEvaluateHealth(script, []*unstructured.Unstructured{sts})
		Expect(results).To(BeEmpty())
	})

	It("reports Progressing while a rolling update is in flight", func() {
		script := loadEvaluateHealth(scriptFile)

		sts := toUnstructured("apps/v1", "StatefulSet", "db", "postgres", map[string]any{
			specKey: map[string]any{replicasKey: float64(3)},
			statusKey: map[string]any{
				readyReplicasKey:   float64(3),
				currentRevisionKey: statefulSetRevision,
				updateRevisionKey:  "postgres-def",
			},
		})

		_, _, results := runEvaluateHealth(script, []*unstructured.Unstructured{sts})
		Expect(results).To(HaveLen(1))
		Expect(results[0].Status).To(Equal(string(libsveltosv1beta1.HealthStatusProgressing)))
	})

	It("reports Progressing when replicas are not all ready", func() {
		script := loadEvaluateHealth(scriptFile)

		sts := toUnstructured("apps/v1", "StatefulSet", "db", "postgres", map[string]any{
			specKey: map[string]any{replicasKey: float64(3)},
			statusKey: map[string]any{
				readyReplicasKey:   float64(1),
				currentRevisionKey: statefulSetRevision,
				updateRevisionKey:  statefulSetRevision,
			},
		})

		_, _, results := runEvaluateHealth(script, []*unstructured.Unstructured{sts})
		Expect(results).To(HaveLen(1))
		Expect(results[0].Status).To(Equal(string(libsveltosv1beta1.HealthStatusProgressing)))
	})

	It("reports a top-level Healthy status when no StatefulSet matched", func() {
		script := loadEvaluateHealth(scriptFile)

		topStatus, _, results := runEvaluateHealth(script, []*unstructured.Unstructured{})
		Expect(results).To(BeEmpty())
		Expect(topStatus).To(Equal(string(libsveltosv1beta1.HealthStatusHealthy)))
	})
})

var _ = Describe("cnpg-cluster.yaml", func() {
	const scriptFile = "cnpg-cluster.yaml"

	It("reports Healthy when the cluster is in a healthy state", func() {
		script := loadEvaluateHealth(scriptFile)

		cluster := toUnstructured("postgresql.cnpg.io/v1", "Cluster", "db", "pg", map[string]any{
			statusKey: map[string]any{phaseKey: "Cluster in healthy state"},
		})

		_, _, results := runEvaluateHealth(script, []*unstructured.Unstructured{cluster})
		Expect(results).To(BeEmpty())
	})

	It("reports Degraded when the cluster is unrecoverable", func() {
		script := loadEvaluateHealth(scriptFile)

		cluster := toUnstructured("postgresql.cnpg.io/v1", "Cluster", "db", "pg", map[string]any{
			statusKey: map[string]any{
				phaseKey: "Cluster is in an unrecoverable state, needs manual intervention",
			},
		})

		_, _, results := runEvaluateHealth(script, []*unstructured.Unstructured{cluster})
		Expect(results).To(HaveLen(1))
		Expect(results[0].Status).To(Equal(string(libsveltosv1beta1.HealthStatusDegraded)))
	})

	It("reports Progressing while the cluster is transitioning", func() {
		script := loadEvaluateHealth(scriptFile)

		cluster := toUnstructured("postgresql.cnpg.io/v1", "Cluster", "db", "pg", map[string]any{
			statusKey: map[string]any{phaseKey: "Creating a new replica"},
		})

		_, _, results := runEvaluateHealth(script, []*unstructured.Unstructured{cluster})
		Expect(results).To(HaveLen(1))
		Expect(results[0].Status).To(Equal(string(libsveltosv1beta1.HealthStatusProgressing)))
	})

	It("reports a top-level Healthy status when no Cluster matched", func() {
		script := loadEvaluateHealth(scriptFile)

		topStatus, _, results := runEvaluateHealth(script, []*unstructured.Unstructured{})
		Expect(results).To(BeEmpty())
		Expect(topStatus).To(Equal(string(libsveltosv1beta1.HealthStatusHealthy)))
	})
})

var _ = Describe("contour-httpproxy.yaml", func() {
	const scriptFile = "contour-httpproxy.yaml"

	It("reports Healthy when the Valid condition is True", func() {
		script := loadEvaluateHealth(scriptFile)

		proxy := toUnstructured("projectcontour.io/v1", "HTTPProxy", "web", "web-proxy", map[string]any{
			statusKey: map[string]any{
				conditionsKey: []any{condition("Valid", "True", "Valid")},
			},
		})

		_, _, results := runEvaluateHealth(script, []*unstructured.Unstructured{proxy})
		Expect(results).To(BeEmpty())
	})

	It("reports Degraded when the Valid condition is False", func() {
		script := loadEvaluateHealth(scriptFile)

		proxy := toUnstructured("projectcontour.io/v1", "HTTPProxy", "web", "web-proxy", map[string]any{
			statusKey: map[string]any{
				conditionsKey: []any{condition("Valid", "False", "ServiceError")},
			},
		})

		_, _, results := runEvaluateHealth(script, []*unstructured.Unstructured{proxy})
		Expect(results).To(HaveLen(1))
		Expect(results[0].Status).To(Equal(string(libsveltosv1beta1.HealthStatusDegraded)))
		Expect(results[0].Message).To(Equal("HTTPProxy web/web-proxy is not Valid (ServiceError)"))
	})

	It("reports a top-level Healthy status when no HTTPProxy matched", func() {
		script := loadEvaluateHealth(scriptFile)

		topStatus, _, results := runEvaluateHealth(script, []*unstructured.Unstructured{})
		Expect(results).To(BeEmpty())
		Expect(topStatus).To(Equal(string(libsveltosv1beta1.HealthStatusHealthy)))
	})
})

var _ = Describe("knative-service.yaml", func() {
	const scriptFile = "knative-service.yaml"

	It("reports Healthy when the Ready condition is True", func() {
		script := loadEvaluateHealth(scriptFile)

		svc := toUnstructured("serving.knative.dev/v1", "Service", "apps", "hello", map[string]any{
			statusKey: map[string]any{
				conditionsKey: []any{condition("Ready", "True", "")},
			},
		})

		_, _, results := runEvaluateHealth(script, []*unstructured.Unstructured{svc})
		Expect(results).To(BeEmpty())
	})

	It("reports Progressing when the Ready condition is Unknown", func() {
		script := loadEvaluateHealth(scriptFile)

		svc := toUnstructured("serving.knative.dev/v1", "Service", "apps", "hello", map[string]any{
			statusKey: map[string]any{
				conditionsKey: []any{condition("Ready", "Unknown", "Deploying")},
			},
		})

		_, _, results := runEvaluateHealth(script, []*unstructured.Unstructured{svc})
		Expect(results).To(HaveLen(1))
		Expect(results[0].Status).To(Equal(string(libsveltosv1beta1.HealthStatusProgressing)))
	})

	It("reports Degraded when the Ready condition is False", func() {
		script := loadEvaluateHealth(scriptFile)

		svc := toUnstructured("serving.knative.dev/v1", "Service", "apps", "hello", map[string]any{
			statusKey: map[string]any{
				conditionsKey: []any{condition("Ready", "False", "RevisionFailed")},
			},
		})

		_, _, results := runEvaluateHealth(script, []*unstructured.Unstructured{svc})
		Expect(results).To(HaveLen(1))
		Expect(results[0].Status).To(Equal(string(libsveltosv1beta1.HealthStatusDegraded)))
	})

	It("reports a top-level Healthy status when no Service matched", func() {
		script := loadEvaluateHealth(scriptFile)

		topStatus, _, results := runEvaluateHealth(script, []*unstructured.Unstructured{})
		Expect(results).To(BeEmpty())
		Expect(topStatus).To(Equal(string(libsveltosv1beta1.HealthStatusHealthy)))
	})
})
