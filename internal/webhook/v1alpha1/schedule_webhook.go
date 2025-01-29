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

	"github.com/robfig/cron/v3"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation/field"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	api "github.com/rclsilver-org/backup-controller/api/v1alpha1"
	"github.com/rclsilver-org/backup-controller/internal/constants"
)

// SetupScheduleWebhookWithManager registers the webhook for Schedule in the manager.
func SetupScheduleWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).For(&api.Schedule{}).
		WithValidator(&ScheduleCustomValidator{
			client: mgr.GetClient(),
		}).
		Complete()
}

// NOTE: The 'path' attribute must follow a specific pattern and should not be modified directly here.
// Modifying the path for an invalid path can cause API server errors; failing to locate the webhook.
// +kubebuilder:webhook:path=/validate-backup-controller-rclsilver-org-github-com-v1alpha1-schedule,mutating=false,failurePolicy=fail,sideEffects=None,groups=backup-controller.rclsilver-org.github.com,resources=schedules,verbs=create;update;delete,versions=v1alpha1,name=vschedule-v1alpha1.kb.io,admissionReviewVersions=v1

// ScheduleCustomValidator struct is responsible for validating the Schedule resource
// when it is created, updated, or deleted.
//
// NOTE: The +kubebuilder:object:generate=false marker prevents controller-gen from generating DeepCopy methods,
// as this struct is used only for temporary operations and does not need to be deeply copied.
type ScheduleCustomValidator struct {
	client client.Client
}

var _ webhook.CustomValidator = &ScheduleCustomValidator{}

// ValidateCreate implements webhook.CustomValidator so a webhook will be registered for the type Schedule.
func (v *ScheduleCustomValidator) ValidateCreate(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	log := log.FromContext(ctx).WithName("schedule-resource")

	schedule, ok := obj.(*api.Schedule)
	if !ok {
		return nil, fmt.Errorf("expected a Schedule object but got %T", obj)
	}

	log.Info("Validation for Schedule upon creation", "name", schedule.GetName())

	return nil, v.validate(schedule)
}

// ValidateUpdate implements webhook.CustomValidator so a webhook will be registered for the type Schedule.
func (v *ScheduleCustomValidator) ValidateUpdate(ctx context.Context, oldObj, newObj runtime.Object) (admission.Warnings, error) {
	log := log.FromContext(ctx).WithName("schedule-resource")

	schedule, ok := newObj.(*api.Schedule)
	if !ok {
		return nil, fmt.Errorf("expected a Schedule object for the newObj but got %T", newObj)
	}

	log.Info("Validation for Schedule upon update", "name", schedule.GetName())

	return nil, v.validate(schedule)
}

// ValidateDelete implements webhook.CustomValidator so a webhook will be registered for the type Schedule.
func (v *ScheduleCustomValidator) ValidateDelete(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	log := log.FromContext(ctx).WithName("schedule-resource")

	schedule, ok := obj.(*api.Schedule)
	if !ok {
		return nil, fmt.Errorf("expected a Schedule object but got %T", obj)
	}

	pods, err := getMutatedPods(ctx, v.client)
	if err != nil {
		return nil, err
	}

	for _, pod := range pods {
		scheduleValue, ok := pod.GetAnnotations()[constants.ScheduleAnnotation]
		if ok && scheduleValue == schedule.GetName() {
			return nil, fmt.Errorf("unable to delete the %q schedule because it is still used", schedule.GetName())
		}
	}

	log.Info("the schedule has been deleted", "name", schedule.GetName())

	return nil, nil
}

// validate validates a given schedule
func (v *ScheduleCustomValidator) validate(schedule *api.Schedule) error {
	var allErrs field.ErrorList

	if _, err := cron.ParseStandard(schedule.Spec.Schedule); err != nil {
		allErrs = append(allErrs, field.Invalid(field.NewPath("spec").Child("schedule"), schedule.Spec.Schedule, err.Error()))
	}

	if len(allErrs) > 0 {
		return apierrors.NewInvalid(
			schema.GroupKind{Group: "backup-controller.rclsilver-org.github.com", Kind: "Schedule"},
			schedule.Name,
			allErrs,
		)
	}

	return nil
}
