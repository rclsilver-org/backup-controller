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

package v1

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"text/template"

	"github.com/Masterminds/sprig/v3"
	"github.com/rclsilver-org/backup-controller/api/v1alpha1"
	"github.com/rclsilver-org/backup-controller/internal/constants"
	corev1 "k8s.io/api/core/v1"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
)

// SetupPodWebhookWithManager registers the webhook for Pod in the manager.
func SetupPodWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).For(&corev1.Pod{}).
		WithDefaulter(&PodCustomDefaulter{
			client: mgr.GetClient(),
		}).
		Complete()
}

// +kubebuilder:webhook:path=/mutate--v1-pod,mutating=true,failurePolicy=fail,sideEffects=None,groups=core,resources=pods,verbs=create,versions=v1,name=mpod-v1.kb.io,admissionReviewVersions=v1
// +kubebuilder:rbac:groups=backup-controller.rclsilver-org.github.com,resources=policies;schedules,verbs=list;get;watch

// PodCustomDefaulter struct is responsible for setting default values on the custom resource of the
// Kind Pod when those are created or updated.
//
// NOTE: The +kubebuilder:object:generate=false marker prevents controller-gen from generating DeepCopy methods,
// as it is used only for temporary operations and does not need to be deeply copied.
type PodCustomDefaulter struct {
	client client.Client
}

var _ webhook.CustomDefaulter = &PodCustomDefaulter{}

// Default implements webhook.CustomDefaulter so a webhook will be registered for the Kind Pod.
func (d *PodCustomDefaulter) Default(ctx context.Context, obj runtime.Object) error {
	log := log.FromContext(ctx).WithName("pod-resource")

	pod, ok := obj.(*corev1.Pod)
	if !ok {
		return fmt.Errorf("expected an Pod object but got %T", obj)
	}

	annotations := pod.GetAnnotations()

	policyName, ok := annotations[constants.PolicyAnnotation]
	if !ok {
		policyName = ""
	}

	scheduleName, ok := annotations[constants.ScheduleAnnotation]
	if !ok {
		scheduleName = ""
	}

	if policyName == "" || scheduleName == "" {
		log.Info("ignoring the pod because either policy or schedule undefined")
		return nil
	}

	sourcePolicy, err := d.getPolicy(ctx, policyName)
	if err != nil {
		return fmt.Errorf("error while fetching the policy: %w", err)
	}

	schedule, err := d.getSchedule(ctx, scheduleName)
	if err != nil {
		return fmt.Errorf("error while fetching the schedule: %w", err)
	}

	policy, err := d.buildTemplate(*sourcePolicy, *pod)
	if err != nil {
		return fmt.Errorf("error while templating the policy: %w", err)
	}

	newContainer := corev1.Container{
		Name: "backup-agent",
	}

	newContainer.Image = policy.Spec.Image.Name
	if policy.Spec.Image.Tag != "" {
		newContainer.Image += ":" + policy.Spec.Image.Tag
	}

	if policy.Spec.Image.PullPolicy != "" {
		newContainer.ImagePullPolicy = policy.Spec.Image.PullPolicy
	} else if policy.Spec.Image.Tag == "" || policy.Spec.Image.Tag == "latest" {
		newContainer.ImagePullPolicy = corev1.PullAlways
	} else {
		newContainer.ImagePullPolicy = corev1.PullIfNotPresent
	}

	newContainer.Env = append(newContainer.Env, policy.Spec.Environment...)

	for _, spec := range policy.Spec.CopyEnv {
		variable, err := d.getContainerEnv(pod, spec.ContainerName, spec.VariableName)
		if err != nil {
			return fmt.Errorf("error while copying environment variable %q from the container %q: %w", spec.VariableName, spec.ContainerName, err)
		}
		if spec.NewName != "" {
			variable.Name = spec.NewName
		}
		newContainer.Env = append(newContainer.Env, variable)
	}

	for _, spec := range policy.Spec.CopyVolumeMount {
		mount, err := d.getContainerVolumeMount(pod, spec.ContainerName, spec.MountPath)
		if err != nil {
			return fmt.Errorf("error while copying volume mount %q from the container %q: %w", spec.MountPath, spec.ContainerName, err)
		}
		newContainer.VolumeMounts = append(newContainer.VolumeMounts, mount)
	}

	newContainer.Env = append(newContainer.Env, corev1.EnvVar{
		Name:  "BC_SCHEDULE",
		Value: schedule.Spec.Schedule,
	})

	pod.Spec.Containers = append(pod.Spec.Containers, newContainer)

	if pod.Labels == nil {
		pod.Labels = make(map[string]string, 1)
	}
	pod.Labels[constants.MutatedLabel] = "true"

	log.Info("spawned the backup agent container")

	return nil
}

func (d *PodCustomDefaulter) getPolicy(ctx context.Context, name string) (*v1alpha1.Policy, error) {
	var policy v1alpha1.Policy

	if err := d.client.Get(ctx, client.ObjectKey{Name: name}, &policy); err != nil {
		return nil, err
	}

	return &policy, nil
}

func (d *PodCustomDefaulter) getSchedule(ctx context.Context, name string) (*v1alpha1.Schedule, error) {
	var schedule v1alpha1.Schedule

	if err := d.client.Get(ctx, client.ObjectKey{Name: name}, &schedule); err != nil {
		return nil, err
	}

	return &schedule, nil
}

func (d *PodCustomDefaulter) getContainerEnv(pod *corev1.Pod, container, variable string) (corev1.EnvVar, error) {
	for _, c := range pod.Spec.Containers {
		if c.Name == container {
			for _, e := range c.Env {
				if e.Name == variable {
					return e, nil
				}
			}
			return corev1.EnvVar{}, fmt.Errorf("variable not found")
		}
	}
	return corev1.EnvVar{}, fmt.Errorf("container not found")
}

func (d *PodCustomDefaulter) getContainerVolumeMount(pod *corev1.Pod, container, mountPath string) (corev1.VolumeMount, error) {
	for _, c := range pod.Spec.Containers {
		if c.Name == container {
			for _, m := range c.VolumeMounts {
				if m.MountPath == mountPath {
					return m, nil
				}
			}
			return corev1.VolumeMount{}, fmt.Errorf("volume mount not found")
		}
	}
	return corev1.VolumeMount{}, fmt.Errorf("container not found")
}

func (d *PodCustomDefaulter) buildTemplate(policy v1alpha1.Policy, pod corev1.Pod) (v1alpha1.Policy, error) {
	podJson, err := json.Marshal(pod)
	if err != nil {
		return v1alpha1.Policy{}, fmt.Errorf("error while marshaling the pod: %w", err)
	}

	var podMap map[string]any
	if err := json.Unmarshal(podJson, &podMap); err != nil {
		return v1alpha1.Policy{}, fmt.Errorf("error while converting the pod: %w", err)
	}

	policyJson, err := json.Marshal(policy)
	if err != nil {
		return v1alpha1.Policy{}, fmt.Errorf("error while marshaling the policy: %w", err)
	}

	policyTpl, err := template.New("base").Funcs(sprig.FuncMap()).Parse(string(policyJson))
	if err != nil {
		return v1alpha1.Policy{}, fmt.Errorf("error while parsing the policy templates: %w", err)
	}

	resultJson := bytes.NewBuffer(nil)
	if err := policyTpl.Execute(resultJson, map[string]any{"pod": podMap}); err != nil {
		return v1alpha1.Policy{}, fmt.Errorf("error while executing the policy templates: %w", err)
	}

	if err := json.Unmarshal(resultJson.Bytes(), &policy); err != nil {
		return v1alpha1.Policy{}, fmt.Errorf("error while unmarshaling the policy: %w", err)
	}

	return policy, nil
}
