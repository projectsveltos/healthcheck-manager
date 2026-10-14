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
	"fmt"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/controller-runtime/pkg/client"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
	logs "github.com/projectsveltos/libsveltos/lib/logsettings"
)

//+kubebuilder:rbac:groups=lib.projectsveltos.io,resources=debuggingconfigurations,verbs=get;list;watch
//+kubebuilder:rbac:groups=apiextensions.k8s.io,resources=customresourcedefinitions,verbs=get;list;watch

const (
	compatibilityErrorMsg = "compatibility checks failed"

	capiClusterKind = "Cluster"
)

func InitScheme() (*runtime.Scheme, error) {
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		return nil, err
	}
	if err := clusterv1.AddToScheme(s); err != nil {
		return nil, err
	}
	if err := configv1beta1.AddToScheme(s); err != nil {
		return nil, err
	}
	if err := libsveltosv1beta1.AddToScheme(s); err != nil {
		return nil, err
	}
	if err := apiextensionsv1.AddToScheme(s); err != nil {
		return nil, err
	}
	return s, nil
}

// addClusterRefs returns matchingClusters followed by the clusters listed in clusterRefs that are
// not already there.
// Only the fields identifying a cluster are kept and APIVersion is set to the current one, so
// that a reference compares equal to the ones built from Sveltos/CAPI Clusters.
// References that are neither a SveltosCluster nor a CAPI Cluster are ignored (and logged):
// clusterproxy.GetClusterType panics on those.
func addClusterRefs(matchingClusters, clusterRefs []corev1.ObjectReference,
	logger logr.Logger) []corev1.ObjectReference {

	seen := make(map[corev1.ObjectReference]bool, len(matchingClusters)+len(clusterRefs))
	for i := range matchingClusters {
		seen[matchingClusters[i]] = true
	}

	for i := range clusterRefs {
		ref, valid := normalizeClusterRef(&clusterRefs[i])
		if !valid {
			logger.V(logs.LogInfo).Info(fmt.Sprintf("ignoring invalid cluster reference %s %s/%s (%s)",
				clusterRefs[i].Kind, clusterRefs[i].Namespace, clusterRefs[i].Name, clusterRefs[i].APIVersion))
			continue
		}
		if seen[ref] {
			continue
		}
		seen[ref] = true
		matchingClusters = append(matchingClusters, ref)
	}

	return matchingClusters
}

// normalizeClusterRef returns a reference to a SveltosCluster or a CAPI Cluster, containing only
// APIVersion, Kind, Namespace and Name. The returned bool is false when ref is not such a reference.
func normalizeClusterRef(ref *corev1.ObjectReference) (corev1.ObjectReference, bool) {
	if ref.Namespace == "" || ref.Name == "" {
		return corev1.ObjectReference{}, false
	}

	gk := schema.FromAPIVersionAndKind(ref.APIVersion, ref.Kind).GroupKind()

	var apiVersion string
	switch {
	case gk.Group == libsveltosv1beta1.GroupVersion.Group && gk.Kind == libsveltosv1beta1.SveltosClusterKind:
		apiVersion = libsveltosv1beta1.GroupVersion.String()
	case gk.Group == clusterv1.GroupVersion.Group && gk.Kind == capiClusterKind:
		apiVersion = clusterv1.GroupVersion.String()
	default:
		return corev1.ObjectReference{}, false
	}

	return corev1.ObjectReference{
		APIVersion: apiVersion,
		Kind:       gk.Kind,
		Namespace:  ref.Namespace,
		Name:       ref.Name,
	}, true
}

func addTypeInformationToObject(scheme *runtime.Scheme, obj client.Object) {
	gvks, _, err := scheme.ObjectKinds(obj)
	if err != nil {
		panic(1)
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
}

// getKeyFromObject returns the Key that can be used in the internal reconciler maps.
func getKeyFromObject(scheme *runtime.Scheme, obj client.Object) *corev1.ObjectReference {
	addTypeInformationToObject(scheme, obj)

	apiVersion, kind := obj.GetObjectKind().GroupVersionKind().ToAPIVersionAndKind()

	return &corev1.ObjectReference{
		Namespace:  obj.GetNamespace(),
		Name:       obj.GetName(),
		Kind:       kind,
		APIVersion: apiVersion,
	}
}
