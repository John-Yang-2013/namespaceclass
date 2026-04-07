/*
Copyright 2026.

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

package controller

import (
	"context"
	"encoding/json"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/discovery"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	platformv1alpha1 "github.com/akuity/namespaceclass/api/v1alpha1"
)

const (
	// LabelManagedBy is stamped on every resource created by this controller.
	LabelManagedBy = "namespaceclass.akuity.io/managed-by"
	// LabelClass records which NamespaceClass created this resource.
	LabelClass = "namespaceclass.akuity.io/class"
	// LabelNamespaceClass is the label on a Namespace that selects which NamespaceClass to use.
	LabelNamespaceClass = "namespaceclass.akuity.io/name"
	// AnnotationAppliedClass records the last class we applied to this namespace.
	AnnotationAppliedClass = "namespaceclass.akuity.io/applied-class"

	fieldManager = "namespaceclass-controller"
)

// NamespaceReconciler reconciles a Namespace object
type NamespaceReconciler struct {
	client.Client
	Scheme          *runtime.Scheme
	DiscoveryClient discovery.DiscoveryInterface
	RESTMapper      meta.RESTMapper
}

// +kubebuilder:rbac:groups=core,resources=namespaces,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=platform.akuity.io,resources=namespaceclasses,verbs=get;list;watch
// +kubebuilder:rbac:groups=*,resources=*,verbs=get;list;watch;create;update;patch;delete

func (r *NamespaceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	// 1. Fetch the Namespace
	ns := &corev1.Namespace{}
	if err := r.Get(ctx, req.NamespacedName, ns); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// 2. Read the desired class from the namespace label
	currentClass := ns.Labels[LabelNamespaceClass]

	// 3. Read the previously applied class from annotation
	previousClass := ns.Annotations[AnnotationAppliedClass]

	log.Info("Reconciling Namespace", "namespace", ns.Name, "currentClass", currentClass, "previousClass", previousClass)

	// 4. If class changed (or removed), clean up resources from the old class
	if previousClass != "" && previousClass != currentClass {
		log.Info("Class changed, cleaning up old resources", "oldClass", previousClass)
		if err := r.deleteResourcesForClass(ctx, ns.Name, previousClass); err != nil {
			return ctrl.Result{}, fmt.Errorf("cleaning up old class %q: %w", previousClass, err)
		}
	}

	// 5. If there is a current class, apply its resources
	if currentClass != "" {
		nc := &platformv1alpha1.NamespaceClass{}
		if err := r.Get(ctx, types.NamespacedName{Name: currentClass}, nc); err != nil {
			if errors.IsNotFound(err) {
				log.Info("NamespaceClass not found, requeueing", "class", currentClass)
				return ctrl.Result{}, fmt.Errorf("NamespaceClass %q not found", currentClass)
			}
			return ctrl.Result{}, err
		}

		if err := r.applyResources(ctx, ns.Name, currentClass, nc.Spec.Resources); err != nil {
			return ctrl.Result{}, fmt.Errorf("applying resources for class %q: %w", currentClass, err)
		}

		if err := r.deleteOrphanedResources(ctx, ns.Name, currentClass, nc.Spec.Resources); err != nil {
			return ctrl.Result{}, fmt.Errorf("deleting orphaned resources for class %q: %w", currentClass, err)
		}
	}

	// 6. Update annotation to record the applied class
	nsCopy := ns.DeepCopy()
	if nsCopy.Annotations == nil {
		nsCopy.Annotations = map[string]string{}
	}
	if currentClass == "" {
		delete(nsCopy.Annotations, AnnotationAppliedClass)
	} else {
		nsCopy.Annotations[AnnotationAppliedClass] = currentClass
	}
	if err := r.Patch(ctx, nsCopy, client.MergeFrom(ns)); err != nil {
		return ctrl.Result{}, fmt.Errorf("updating applied-class annotation: %w", err)
	}

	log.Info("Reconciliation complete", "namespace", ns.Name, "class", currentClass)
	return ctrl.Result{}, nil
}

// applyResources creates or updates all resources defined in the NamespaceClass
// into the target namespace using Server-Side Apply.
func (r *NamespaceReconciler) applyResources(ctx context.Context, namespace, className string, resources []runtime.RawExtension) error {
	log := logf.FromContext(ctx)

	for _, rawResource := range resources {
		obj := &unstructured.Unstructured{}
		if err := json.Unmarshal(rawResource.Raw, obj); err != nil {
			return fmt.Errorf("decoding resource: %w", err)
		}

		// Determine if this is a namespace-scoped resource and set namespace accordingly
		namespaced, err := r.isNamespaced(obj.GroupVersionKind())
		if err != nil {
			return fmt.Errorf("checking scope of %s %s: %w", obj.GetKind(), obj.GetName(), err)
		}
		if namespaced {
			obj.SetNamespace(namespace)
		}

		// Stamp our tracking labels
		labels := obj.GetLabels()
		if labels == nil {
			labels = map[string]string{}
		}
		labels[LabelManagedBy] = "true"
		labels[LabelClass] = className
		obj.SetLabels(labels)

		log.Info("Applying resource", "kind", obj.GetKind(), "name", obj.GetName(), "namespace", obj.GetNamespace())
		if err := r.Patch(ctx, obj, client.Apply, client.FieldOwner(fieldManager), client.ForceOwnership); err != nil {
			return fmt.Errorf("applying %s/%s: %w", obj.GetKind(), obj.GetName(), err)
		}
	}
	return nil
}

// deleteResourcesForClass removes all resources in the namespace that were created by a given class.
func (r *NamespaceReconciler) deleteResourcesForClass(ctx context.Context, namespace, className string) error {
	return r.deleteMatchingResources(ctx, namespace, client.MatchingLabels{
		LabelManagedBy: "true",
		LabelClass:     className,
	})
}

// deleteOrphanedResources removes resources that were previously created for a class but
// are no longer defined in the current NamespaceClass spec (name-set diff).
func (r *NamespaceReconciler) deleteOrphanedResources(ctx context.Context, namespace, className string, desired []runtime.RawExtension) error {
	log := logf.FromContext(ctx)

	// Build a set of desired resource identities: "Kind/name"
	desiredSet := map[string]bool{}
	for _, rawResource := range desired {
		obj := &unstructured.Unstructured{}
		if err := json.Unmarshal(rawResource.Raw, obj); err != nil {
			continue
		}
		desiredSet[obj.GetKind()+"/"+obj.GetName()] = true
	}

	// List all resources we own for this class and delete any not in the desired set
	return r.deleteMatchingResources(ctx, namespace, client.MatchingLabels{
		LabelManagedBy: "true",
		LabelClass:     className,
	}, func(obj *unstructured.Unstructured) bool {
		key := obj.GetKind() + "/" + obj.GetName()
		if !desiredSet[key] {
			log.Info("Deleting orphaned resource", "kind", obj.GetKind(), "name", obj.GetName())
			return true
		}
		return false
	})
}

// deleteMatchingResources iterates over all namespace-scoped resource types, lists objects
// matching the given labels, and deletes those for which the optional filter returns true
// (or all of them if no filter is provided).
func (r *NamespaceReconciler) deleteMatchingResources(ctx context.Context, namespace string, labels client.MatchingLabels, filters ...func(*unstructured.Unstructured) bool) error {
	// Discover all API resources in the cluster
	_, resourceLists, err := r.DiscoveryClient.ServerGroupsAndResources()
	if err != nil {
		// Partial discovery errors are common; proceed with what we got
		if !discovery.IsGroupDiscoveryFailedError(err) {
			return fmt.Errorf("discovering API resources: %w", err)
		}
	}

	for _, rl := range resourceLists {
		gv, err := schema.ParseGroupVersion(rl.GroupVersion)
		if err != nil {
			continue
		}
		for _, resource := range rl.APIResources {
			// Only process namespace-scoped, listable, deletable resources
			if !resource.Namespaced || !containsVerb(resource.Verbs, "list") || !containsVerb(resource.Verbs, "delete") {
				continue
			}

			list := &unstructured.UnstructuredList{}
			list.SetGroupVersionKind(schema.GroupVersionKind{
				Group:   gv.Group,
				Version: gv.Version,
				Kind:    resource.Kind + "List",
			})

			if err := r.List(ctx, list, client.InNamespace(namespace), labels); err != nil {
				// Ignore permission errors for resource types we don't have access to
				if errors.IsForbidden(err) || errors.IsMethodNotSupported(err) {
					continue
				}
				return fmt.Errorf("listing %s in namespace %s: %w", resource.Kind, namespace, err)
			}

			for i := range list.Items {
				obj := &list.Items[i]
				shouldDelete := true
				for _, filter := range filters {
					if !filter(obj) {
						shouldDelete = false
						break
					}
				}
				if shouldDelete {
					if err := r.Delete(ctx, obj); err != nil && !errors.IsNotFound(err) {
						return fmt.Errorf("deleting %s/%s: %w", obj.GetKind(), obj.GetName(), err)
					}
				}
			}
		}
	}
	return nil
}

// isNamespaced returns true if the given GVK is namespace-scoped.
func (r *NamespaceReconciler) isNamespaced(gvk schema.GroupVersionKind) (bool, error) {
	mapping, err := r.RESTMapper.RESTMapping(gvk.GroupKind(), gvk.Version)
	if err != nil {
		return false, err
	}
	return mapping.Scope.Name() == meta.RESTScopeNameNamespace, nil
}

// namespacesUsingClass maps a NamespaceClass event to reconcile requests for all
// Namespaces that reference that class via label.
func (r *NamespaceReconciler) namespacesUsingClass(ctx context.Context, obj client.Object) []reconcile.Request {
	log := logf.FromContext(ctx)
	className := obj.GetName()

	nsList := &corev1.NamespaceList{}
	if err := r.List(ctx, nsList, client.MatchingLabels{LabelNamespaceClass: className}); err != nil {
		log.Error(err, "Could not list Namespaces for NamespaceClass", "class", className)
		return nil
	}

	requests := make([]reconcile.Request, 0, len(nsList.Items))
	for _, ns := range nsList.Items {
		requests = append(requests, reconcile.Request{
			NamespacedName: types.NamespacedName{Name: ns.Name},
		})
	}
	return requests
}

func containsVerb(verbs []string, verb string) bool {
	for _, v := range verbs {
		if v == verb {
			return true
		}
	}
	return false
}

// SetupWithManager sets up the controller with the Manager.
func (r *NamespaceReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.Namespace{}).
		Watches(
			&platformv1alpha1.NamespaceClass{},
			handler.EnqueueRequestsFromMapFunc(r.namespacesUsingClass),
		).
		Named("namespace").
		Complete(r)
}
