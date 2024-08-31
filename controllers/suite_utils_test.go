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
	"fmt"
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/cluster-api/util"
	"sigs.k8s.io/controller-runtime/pkg/client"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	"github.com/projectsveltos/healthcheck-manager/controllers"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
	libsveltosset "github.com/projectsveltos/libsveltos/lib/set"
)

var (
	cacheSyncBackoff = wait.Backoff{
		Duration: 100 * time.Millisecond,
		Factor:   1.5,
		Steps:    8,
		Jitter:   0.4,
	}
)

const (
	sveltosKubeconfigPostfix = "-kubeconfig"
)

func randomString() string {
	const length = 10
	return util.RandomString(length)
}

func setupScheme() (*runtime.Scheme, error) {
	s := runtime.NewScheme()
	if err := configv1beta1.AddToScheme(s); err != nil {
		return nil, err
	}
	if err := clusterv1.AddToScheme(s); err != nil {
		return nil, err
	}
	if err := clientgoscheme.AddToScheme(s); err != nil {
		return nil, err
	}
	if err := apiextensionsv1.AddToScheme(s); err != nil {
		return nil, err
	}
	if err := libsveltosv1beta1.AddToScheme(s); err != nil {
		return nil, err
	}
	return s, nil
}

// waitForObject waits for the cache to be updated helps in preventing test flakes due to the cache sync delays.
func waitForObject(ctx context.Context, c client.Client, obj client.Object) error {
	// Makes sure the cache is updated with the new object
	objCopy := obj.DeepCopyObject().(client.Object)
	key := client.ObjectKeyFromObject(obj)
	if err := wait.ExponentialBackoff(
		cacheSyncBackoff,
		func() (done bool, err error) {
			if err := c.Get(ctx, key, objCopy); err != nil {
				if apierrors.IsNotFound(err) {
					return false, nil
				}
				return false, err
			}
			return true, nil
		}); err != nil {
		return errors.Wrapf(err, "object %s, %s is not being added to the testenv client cache", obj.GetObjectKind().GroupVersionKind().String(), key)
	}
	return nil
}

func addTypeInformationToObject(scheme *runtime.Scheme, obj client.Object) error {
	gvks, _, err := scheme.ObjectKinds(obj)
	if err != nil {
		return fmt.Errorf("missing apiVersion or kind and cannot assign it; %w", err)
	}

	for _, gvk := range gvks {
		if gvk.Kind == "" {
			continue
		}
		if gvk.Version == "" || gvk.Version == runtime.APIVersionInternal {
			continue
		}
		obj.GetObjectKind().SetGroupVersionKind(gvk)
		break
	}

	return nil
}

func getClusterHealthCheckReconciler(c client.Client) *controllers.ClusterHealthCheckReconciler {
	return &controllers.ClusterHealthCheckReconciler{
		Client:              c,
		Scheme:              scheme,
		ClusterMap:          make(map[corev1.ObjectReference]*libsveltosset.Set),
		CHCToClusterMap:     make(map[types.NamespacedName]*libsveltosset.Set),
		ClusterHealthChecks: make(map[corev1.ObjectReference]libsveltosv1beta1.Selector),
		HealthCheckMap:      make(map[corev1.ObjectReference]*libsveltosset.Set),
		CHCToHealthCheckMap: make(map[types.NamespacedName]*libsveltosset.Set),
		ClusterLabels:       make(map[corev1.ObjectReference]map[string]string),
		Mux:                 sync.Mutex{},
	}
}

func getHealthCheckInstance(name string) *libsveltosv1beta1.HealthCheck {
	return &libsveltosv1beta1.HealthCheck{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: libsveltosv1beta1.HealthCheckSpec{
			ResourceSelectors: []libsveltosv1beta1.ResourceSelector{
				{
					Group:    randomString(),
					Version:  randomString(),
					Kind:     randomString(),
					Evaluate: randomString(),
				},
			},
			EvaluateHealth: randomString(),
		},
	}
}

func getHealthCheckReport(healthCheckName, clusterNamespace, clusterName string) *libsveltosv1beta1.HealthCheckReport {
	return &libsveltosv1beta1.HealthCheckReport{
		ObjectMeta: metav1.ObjectMeta{
			Name: randomString(),
			Labels: map[string]string{
				libsveltosv1beta1.HealthCheckNameLabel: healthCheckName,
			},
		},
		Spec: libsveltosv1beta1.HealthCheckReportSpec{
			ClusterNamespace: clusterNamespace,
			ClusterName:      clusterName,
			HealthCheckName:  healthCheckName,
			ClusterType:      libsveltosv1beta1.ClusterTypeCapi,
		},
	}
}

func getReloaderReport(clusterNamespace, clusterName string,
	clusterType *libsveltosv1beta1.ClusterType) *libsveltosv1beta1.ReloaderReport {

	return &libsveltosv1beta1.ReloaderReport{
		ObjectMeta: metav1.ObjectMeta{
			Name:   randomString(),
			Labels: libsveltosv1beta1.GetReloaderReportLabels(clusterName, clusterType),
		},
		Spec: libsveltosv1beta1.ReloaderReportSpec{
			ClusterNamespace: clusterNamespace,
			ClusterName:      clusterName,
			ClusterType:      *clusterType,
		},
	}
}

func prepareCluster() *clusterv1.Cluster {
	namespace := randomString()
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: namespace,
		},
	}
	Expect(testEnv.Create(context.TODO(), ns)).To(Succeed())
	Expect(waitForObject(context.TODO(), testEnv.Client, ns)).To(Succeed())

	cluster := &clusterv1.Cluster{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      randomString(),
		},
	}

	machine := &clusterv1.Machine{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      randomString(),
			Labels: map[string]string{
				clusterv1.ClusterNameLabel:         cluster.Name,
				clusterv1.MachineControlPlaneLabel: "ok",
			},
		},
	}

	Expect(testEnv.Create(context.TODO(), cluster)).To(Succeed())
	Expect(testEnv.Create(context.TODO(), machine)).To(Succeed())
	Expect(waitForObject(context.TODO(), testEnv.Client, ns)).To(Succeed())

	cluster.Status = clusterv1.ClusterStatus{
		InfrastructureReady: true,
		ControlPlaneReady:   true,
	}
	Expect(testEnv.Status().Update(context.TODO(), cluster)).To(Succeed())

	machine.Status = clusterv1.MachineStatus{
		Phase: string(clusterv1.MachinePhaseRunning),
	}
	Expect(testEnv.Status().Update(context.TODO(), machine)).To(Succeed())

	// Create a secret with cluster kubeconfig

	By("Create the secret with cluster kubeconfig")
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: cluster.Namespace,
			Name:      cluster.Name + sveltosKubeconfigPostfix,
		},
		Data: map[string][]byte{
			"value": testEnv.Kubeconfig,
		},
	}
	Expect(testEnv.Client.Create(context.TODO(), secret)).To(Succeed())
	Expect(waitForObject(context.TODO(), testEnv.Client, secret)).To(Succeed())

	Expect(addTypeInformationToObject(scheme, cluster)).To(Succeed())

	return cluster
}

func getClusterRef(cluster client.Object) *corev1.ObjectReference {
	apiVersion, kind := cluster.GetObjectKind().GroupVersionKind().ToAPIVersionAndKind()
	return &corev1.ObjectReference{
		Namespace:  cluster.GetNamespace(),
		Name:       cluster.GetName(),
		APIVersion: apiVersion,
		Kind:       kind,
	}
}
