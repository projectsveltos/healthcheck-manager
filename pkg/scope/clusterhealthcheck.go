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

package scope

import (
	"context"

	"github.com/go-logr/logr"
	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/cluster-api/util/patch"
	"sigs.k8s.io/controller-runtime/pkg/client"

	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
)

// ClusterHealthCheckScopeParams defines the input parameters used to create a new ClusterHealthCheck Scope.
type ClusterHealthCheckScopeParams struct {
	Client             client.Client
	Logger             logr.Logger
	ClusterHealthCheck *libsveltosv1beta1.ClusterHealthCheck
	ControllerName     string
}

// NewClusterHealthCheckScope creates a new ClusterHealthCheck Scope from the supplied parameters.
// This is meant to be called for each reconcile iteration.
func NewClusterHealthCheckScope(params ClusterHealthCheckScopeParams) (*ClusterHealthCheckScope, error) {
	if params.Client == nil {
		return nil, errors.New("client is required when creating a ClusterHealthCheckScope")
	}
	if params.ClusterHealthCheck == nil {
		return nil, errors.New("failed to generate new scope from nil ClusterHealthCheck")
	}

	helper, err := patch.NewHelper(params.ClusterHealthCheck, params.Client)
	if err != nil {
		return nil, errors.Wrap(err, "failed to init patch helper")
	}
	return &ClusterHealthCheckScope{
		Logger:             params.Logger,
		client:             params.Client,
		ClusterHealthCheck: params.ClusterHealthCheck,
		patchHelper:        helper,
		controllerName:     params.ControllerName,
	}, nil
}

// ClusterHealthCheckScope defines the basic context for an actuator to operate upon.
type ClusterHealthCheckScope struct {
	logr.Logger
	client             client.Client
	patchHelper        *patch.Helper
	ClusterHealthCheck *libsveltosv1beta1.ClusterHealthCheck
	controllerName     string
}

// PatchObject persists the feature configuration and status.
func (s *ClusterHealthCheckScope) PatchObject(ctx context.Context) error {
	return s.patchHelper.Patch(
		ctx,
		s.ClusterHealthCheck,
	)
}

// Close closes the current scope persisting the ClusterHealthCheck configuration and status.
func (s *ClusterHealthCheckScope) Close(ctx context.Context) error {
	return s.PatchObject(ctx)
}

// Name returns the ClusterHealthCheck name.
func (s *ClusterHealthCheckScope) Name() string {
	return s.ClusterHealthCheck.Name
}

// ControllerName returns the name of the controller that
// created the ClusterHealthCheckScope.
func (s *ClusterHealthCheckScope) ControllerName() string {
	return s.controllerName
}

// GetSelector returns the ClusterSelector
func (s *ClusterHealthCheckScope) GetSelector() *metav1.LabelSelector {
	return &s.ClusterHealthCheck.Spec.ClusterSelector.LabelSelector
}

// SetMatchingClusterRefs sets the feature status.
func (s *ClusterHealthCheckScope) SetMatchingClusterRefs(matchingClusters []corev1.ObjectReference) {
	s.ClusterHealthCheck.Status.MatchingClusterRefs = matchingClusters
}

// SetClusterConditions sets the ClusterConditions Status field.
func (s *ClusterHealthCheckScope) SetClusterConditions(clusterConditions []libsveltosv1beta1.ClusterCondition) {
	s.ClusterHealthCheck.Status.ClusterConditions = clusterConditions
}
