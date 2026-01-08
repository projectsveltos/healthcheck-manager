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
	"github.com/pkg/errors"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/cluster-api/util"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
	"github.com/projectsveltos/libsveltos/lib/clusterproxy"
	logs "github.com/projectsveltos/libsveltos/lib/logsettings"
)

const (
	sveltosEnv = "PROJECTSVELTOS"
)

// ReloaderReportReconciler reconciles a ReloaderReport object
type ReloaderReportReconciler struct {
	client.Client
	Scheme                *runtime.Scheme
	ReloaderReportMode    ReportMode
	ShardKey              string // when set, only clusters matching the ShardKey will be reconciled
	CapiOnboardAnnotation string // when set, only capi clusters with this annotation are considered
	Version               string
}

//+kubebuilder:rbac:groups=lib.projectsveltos.io,resources=reloaderreports,verbs=create;get;list;watch;update;patch;delete

func (r *ReloaderReportReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := ctrl.LoggerFrom(ctx)
	logger.V(logs.LogDebug).Info("Reconciling ReloaderReport")

	// Fecth the ReloaderReport instance
	reloaderReport := &libsveltosv1beta1.ReloaderReport{}
	if err := r.Get(ctx, req.NamespacedName, reloaderReport); err != nil {
		if apierrors.IsNotFound(err) {
			return reconcile.Result{}, nil
		}
		logger.Error(err, "Failed to fetch ReloaderReport")
		return reconcile.Result{}, errors.Wrapf(
			err,
			"Failed to fetch ReloaderReport %s",
			req.NamespacedName,
		)
	}

	if !reloaderReport.DeletionTimestamp.IsZero() {
		return reconcile.Result{}, nil
	}

	// ReloaderReport contains a set of resources (Deployments/StatefulSets/DaemonSets) that need a
	// rolling upgrade to be triggered.
	// processReloaderReport fetches each one of the listed resources from the managed cluster and
	// updates its containers' envs causing rolling upgrade.
	// When done, if all listed resources (still existing in the managed cluster) were updated, this
	// method deletes the ReloaderReport in the management cluster. If update failed for one or more
	// resource, ReloaderReport Spec in the management cluster is updated to contain only the failed
	// resource
	if err := r.processReloaderReport(ctx, reloaderReport, logger); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *ReloaderReportReconciler) SetupWithManager(mgr ctrl.Manager, collectionInterval int) error {
	if r.ReloaderReportMode == CollectFromManagementCluster {
		go collectReloaderReports(mgr.GetClient(), collectionInterval, r.ShardKey, r.CapiOnboardAnnotation,
			r.Version, mgr.GetLogger())
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&libsveltosv1beta1.ReloaderReport{}).
		Complete(r)
}

// processReloaderReport triggers a rolling upgrade in the managed cluster for any Deployment,
// StatefulSet and DaemonSet instance listed in the ReloaderReport
func (r *ReloaderReportReconciler) processReloaderReport(ctx context.Context,
	reloaderReport *libsveltosv1beta1.ReloaderReport, logger logr.Logger) error {

	logger = logger.WithValues("cluster", fmt.Sprintf("%s/%s",
		reloaderReport.Spec.ClusterNamespace, reloaderReport.Spec.ClusterName))

	if reloaderReport.Annotations != nil {
		// Resource (ConfigMap or Secret) causing this rolling upgrade to be triggered
		kind := reloaderReport.Annotations[libsveltosv1beta1.ReloaderReportResourceKindAnnotation]
		namespace := reloaderReport.Annotations[libsveltosv1beta1.ReloaderReportResourceKindAnnotation]
		name := reloaderReport.Annotations[libsveltosv1beta1.ReloaderReportResourceKindAnnotation]
		logger = logger.WithValues("mountedResource", fmt.Sprintf("%s:%s/%s",
			kind, namespace, name))
	}

	remoteClient, err := clusterproxy.GetKubernetesClient(ctx, r.Client, reloaderReport.Spec.ClusterNamespace,
		reloaderReport.Spec.ClusterName, "", "", reloaderReport.Spec.ClusterType, logger)
	if err != nil {
		return err
	}

	value := randomString()

	// Save resources listed in ReloaderReport that reconciliation failed to update
	failed := make([]libsveltosv1beta1.ReloaderInfo, 0)

	for i := range reloaderReport.Spec.ResourcesToReload {
		rr := &reloaderReport.Spec.ResourcesToReload[i]
		err = r.triggerRollingUpgrade(ctx, remoteClient, rr, value, logger)
		if err != nil {
			logger.V(logs.LogInfo).Error(err, "triggering rolling upgrade failed")
			failed = append(failed, *rr)
		} else {
			logger.V(logs.LogInfo).Info("rolling upgrade triggered")
		}
	}

	if len(failed) == 0 {
		// ReloaderReport can be deleted
		return r.Delete(ctx, reloaderReport)
	}

	// Update ReloaderReport with only failed ones
	reloaderReport.Spec.ResourcesToReload = failed
	return r.Update(ctx, reloaderReport)
}

func (r *ReloaderReportReconciler) triggerRollingUpgrade(ctx context.Context, remoteClient client.Client,
	reloaderInfo *libsveltosv1beta1.ReloaderInfo, value string, logger logr.Logger) error {

	logger = logger.WithValues("kind", reloaderInfo.Kind)
	logger = logger.WithValues("resource", fmt.Sprintf("%s/%s", reloaderInfo.Namespace, reloaderInfo.Name))

	logger.V(logs.LogInfo).Info(fmt.Sprintf("trigger rolling updgrade (value %s)", value))

	resourceName := types.NamespacedName{Namespace: reloaderInfo.Namespace, Name: reloaderInfo.Name}
	var obj client.Object
	var err error
	switch reloaderInfo.Kind {
	case "Deployment":
		obj, err = r.fetchAndPrepareDeployment(ctx, remoteClient, &resourceName, value, logger)
	case "StatefulSet":
		obj, err = r.fetchAndPrepareStatefulSet(ctx, remoteClient, &resourceName, value, logger)
	case "DaemonSet":
		obj, err = r.fetchAndPrepareDaemonSet(ctx, remoteClient, &resourceName, value, logger)
	}

	if err != nil {
		// If resource was not found in the managed cluster ignore error
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}

	return remoteClient.Update(ctx, obj)
}

func (r *ReloaderReportReconciler) fetchAndPrepareDeployment(ctx context.Context, remoteClient client.Client,
	resourceName *types.NamespacedName, value string, logger logr.Logger) (client.Object, error) {

	depl := &appsv1.Deployment{}
	err := remoteClient.Get(ctx, *resourceName, depl)
	if err != nil {
		return nil, err
	}

	for i := range depl.Spec.Template.Spec.Containers {
		depl.Spec.Template.Spec.Containers[i].Env =
			r.updateEnvs(depl.Spec.Template.Spec.Containers[i].Env, value)
	}

	return depl, nil
}

func (r *ReloaderReportReconciler) fetchAndPrepareStatefulSet(ctx context.Context, remoteClient client.Client,
	resourceName *types.NamespacedName, value string, logger logr.Logger) (client.Object, error) {

	statefulSet := &appsv1.StatefulSet{}
	err := remoteClient.Get(ctx, *resourceName, statefulSet)
	if err != nil {
		return nil, err
	}

	for i := range statefulSet.Spec.Template.Spec.Containers {
		statefulSet.Spec.Template.Spec.Containers[i].Env =
			r.updateEnvs(statefulSet.Spec.Template.Spec.Containers[i].Env, value)
	}

	return statefulSet, nil
}

func (r *ReloaderReportReconciler) fetchAndPrepareDaemonSet(ctx context.Context, remoteClient client.Client,
	resourceName *types.NamespacedName, value string, logger logr.Logger) (client.Object, error) {

	daemonSet := &appsv1.DaemonSet{}
	err := remoteClient.Get(ctx, *resourceName, daemonSet)
	if err != nil {
		return nil, err
	}

	for i := range daemonSet.Spec.Template.Spec.Containers {
		daemonSet.Spec.Template.Spec.Containers[i].Env =
			r.updateEnvs(daemonSet.Spec.Template.Spec.Containers[i].Env, value)
	}

	return daemonSet, nil
}

// updateEnvs adds or updates sveltosEnv env
func (r *ReloaderReportReconciler) updateEnvs(envs []corev1.EnvVar, value string) []corev1.EnvVar {
	if envs == nil {
		envs = make([]corev1.EnvVar, 0)
	}

	for i := range envs {
		if envs[i].Name == sveltosEnv {
			envs[i].Value = value
			return envs
		}
	}

	envs = append(envs, corev1.EnvVar{
		Name:  sveltosEnv,
		Value: value,
	})

	return envs
}

func randomString() string {
	const length = 10
	return util.RandomString(length)
}
