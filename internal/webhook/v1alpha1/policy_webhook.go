/*
Copyright 2025.

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

package v1alpha1

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	api "github.com/rclsilver-org/backup-controller/api/v1alpha1"
	"github.com/rclsilver-org/backup-controller/internal/constants"
)

// SetupPolicyWebhookWithManager registers the webhook for Policy in the manager.
func SetupPolicyWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).For(&api.Policy{}).
		WithValidator(&PolicyCustomValidator{
			client: mgr.GetClient(),
		}).
		Complete()
}

// NOTE: The 'path' attribute must follow a specific pattern and should not be modified directly here.
// Modifying the path for an invalid path can cause API server errors; failing to locate the webhook.
// +kubebuilder:webhook:path=/validate-backup-controller-rclsilver-org-github-com-v1alpha1-policy,mutating=false,failurePolicy=fail,sideEffects=None,groups=backup-controller.rclsilver-org.github.com,resources=policies,verbs=create;update;delete,versions=v1alpha1,name=vpolicy-v1alpha1.kb.io,admissionReviewVersions=v1

// PolicyCustomValidator struct is responsible for validating the Policy resource
// when it is created, updated, or deleted.
//
// NOTE: The +kubebuilder:object:generate=false marker prevents controller-gen from generating DeepCopy methods,
// as this struct is used only for temporary operations and does not need to be deeply copied.
type PolicyCustomValidator struct {
	client client.Client
}

var _ webhook.CustomValidator = &PolicyCustomValidator{}

// ValidateCreate implements webhook.CustomValidator so a webhook will be registered for the type Policy.
func (v *PolicyCustomValidator) ValidateCreate(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	log := log.FromContext(ctx).WithName("policy-resource")

	policy, ok := obj.(*api.Policy)
	if !ok {
		return nil, fmt.Errorf("expected a Policy object but got %T", obj)
	}

	log.Info("Validation for Policy upon creation", "name", policy.GetName())

	return nil, v.validate(policy)
}

// ValidateUpdate implements webhook.CustomValidator so a webhook will be registered for the type Policy.
func (v *PolicyCustomValidator) ValidateUpdate(ctx context.Context, oldObj, newObj runtime.Object) (admission.Warnings, error) {
	log := log.FromContext(ctx).WithName("policy-resource")

	policy, ok := newObj.(*api.Policy)
	if !ok {
		return nil, fmt.Errorf("expected a Policy object for the newObj but got %T", newObj)
	}

	log.Info("Validation for Policy upon update", "name", policy.GetName())

	return nil, v.validate(policy)
}

// ValidateDelete implements webhook.CustomValidator so a webhook will be registered for the type Policy.
func (v *PolicyCustomValidator) ValidateDelete(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	log := log.FromContext(ctx).WithName("policy-resource")

	policy, ok := obj.(*api.Policy)
	if !ok {
		return nil, fmt.Errorf("expected a Policy object but got %T", obj)
	}

	pods, err := getMutatedPods(ctx, v.client)
	if err != nil {
		return nil, err
	}

	for _, pod := range pods {
		policyValue, ok := pod.GetAnnotations()[constants.PolicyAnnotation]
		if ok && policyValue == policy.GetName() {
			return nil, fmt.Errorf("unable to delete the %q policy because it is still used", policy.GetName())
		}
	}

	log.Info("the policy has been deleted", "name", policy.GetName())

	return nil, nil
}

// validate validates a given policy
func (v *PolicyCustomValidator) validate(policy *api.Policy) error {
	return nil
}
