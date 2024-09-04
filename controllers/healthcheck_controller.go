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

	"github.com/pkg/errors"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
	logs "github.com/projectsveltos/libsveltos/lib/logsettings"
)

// HealthCheckReconciler reconciles a HealthCheck object
type HealthCheckReconciler struct {
	client.Client
	Scheme                *runtime.Scheme
	HealthCheckReportMode ReportMode
	ShardKey              string // when set, only clusters matching the ShardKey will be reconciled
	Version               string
}

// +kubebuilder:rbac:groups=lib.projectsveltos.io,resources=healthchecks,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=lib.projectsveltos.io,resources=healthchecks/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=lib.projectsveltos.io,resources=healthchecks/finalizers,verbs=update
func (r *HealthCheckReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := ctrl.LoggerFrom(ctx)
	logger.V(logs.LogInfo).Info("Reconciling HealthCheck")

	// Fecth the HealthCheck instance
	healthCheck := &libsveltosv1beta1.HealthCheck{}
	if err := r.Get(ctx, req.NamespacedName, healthCheck); err != nil {
		if apierrors.IsNotFound(err) {
			err = removeHealthCheckReports(ctx, r.Client, healthCheck, logger)
			if err != nil {
				return reconcile.Result{}, err
			}
			return reconcile.Result{}, nil
		}
		logger.Error(err, "Failed to fetch healthCheck")
		return reconcile.Result{}, errors.Wrapf(
			err,
			"Failed to fetch healthCheck %s",
			req.NamespacedName,
		)
	}

	// Handle deleted healthCheck
	if !healthCheck.DeletionTimestamp.IsZero() {
		err := removeHealthCheckReports(ctx, r.Client, healthCheck, logger)
		if err != nil {
			return reconcile.Result{}, err
		}
		return reconcile.Result{}, nil
	}

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *HealthCheckReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if r.HealthCheckReportMode == CollectFromManagementCluster {
		go collectHealthCheckReports(mgr.GetClient(), r.ShardKey, r.Version, mgr.GetLogger())
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&libsveltosv1beta1.HealthCheck{}).
		Complete(r)
}
