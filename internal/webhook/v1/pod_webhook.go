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
	"regexp"
	"slices"
	"strconv"

	"text/template"

	"github.com/Masterminds/sprig/v3"
	"github.com/rclsilver-org/backup-controller/api/v1alpha1"
	"github.com/rclsilver-org/backup-controller/internal/constants"
	corev1 "k8s.io/api/core/v1"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
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

	filterPattern, ok := annotations[constants.FilterAnnotation]
	if !ok {
		filterPattern = ""
	}

	if policyName == "" || scheduleName == "" {
		log.Info("ignoring the pod because either policy or schedule undefined")
		return nil
	}

	if filterPattern != "" {
		matched, err := regexp.MatchString(filterPattern, pod.Name)
		if err != nil {
			return fmt.Errorf("invalid filter pattern %q: %w", filterPattern, err)
		}
		if !matched {
			log.Info("ignoring the pod because name does not match filter pattern", "podName", pod.Name, "filter", filterPattern)
			return nil
		}
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

	newContainer.Image, newContainer.ImagePullPolicy = resolveImage(policy.Spec.Image)

	if policy.Spec.AutoDetectVolumeMounts {
		mounts, err := d.getDetectedVolumeMounts(*pod)
		if err != nil {
			return fmt.Errorf("error while detecting volume mounts: %w", err)
		}

		var backupDir string
		if len(mounts) == 1 {
			log.Info("detected volume mount", "path", mounts[0].MountPath)
			backupDir = mounts[0].MountPath
		} else {
			log.Info(fmt.Sprintf("detected %d volume mounts", len(mounts)))
			for i, m := range mounts {
				if i > 0 {
					backupDir += ":"
				}
				backupDir += m.MountPath
			}
		}

		newContainer.VolumeMounts = append(newContainer.VolumeMounts, mounts...)
		newContainer.Env = append(newContainer.Env, corev1.EnvVar{
			Name:  "BC_BACKUP_DIR",
			Value: backupDir,
		})
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

	if err := d.applyRetentionDays(annotations, &newContainer); err != nil {
		return err
	}

	var zero int64 = 0
	var false bool = false
	newContainer.SecurityContext = &corev1.SecurityContext{
		RunAsUser:    &zero,
		RunAsGroup:   &zero,
		RunAsNonRoot: &false,
	}

	// Health checks for the agent are opt-in per policy: the right check depends
	// on the agent image (crond-based agents vs. the plain cnpg binary), so no
	// default is applied here.
	newContainer.LivenessProbe = policy.Spec.LivenessProbe
	newContainer.ReadinessProbe = policy.Spec.ReadinessProbe
	newContainer.StartupProbe = policy.Spec.StartupProbe

	pod.Spec.Containers = append(pod.Spec.Containers, newContainer)

	// Optionally inject a metrics exporter sidecar that shares the agent's
	// environment (restic credentials + repository) and exposes Prometheus metrics.
	if policy.Spec.Exporter != nil {
		pod.Spec.Containers = append(pod.Spec.Containers, buildExporterContainer(policy.Spec.Exporter, newContainer.Env))

		if pod.Labels == nil {
			pod.Labels = make(map[string]string, 1)
		}
		pod.Labels[constants.ExporterLabel] = "true"

		log.Info("spawned the restic exporter container")
	}

	if pod.Labels == nil {
		pod.Labels = make(map[string]string, 1)
	}
	pod.Labels[constants.MutatedLabel] = "true"

	log.Info("spawned the backup agent container")

	return nil
}

// resolveImage returns the full image reference and pull policy for an Image
// spec: Always for an empty/"latest" tag, IfNotPresent otherwise, unless an
// explicit pull policy is set.
func resolveImage(img v1alpha1.Image) (string, corev1.PullPolicy) {
	ref := img.Name
	if img.Tag != "" {
		ref += ":" + img.Tag
	}

	switch {
	case img.PullPolicy != "":
		return ref, img.PullPolicy
	case img.Tag == "" || img.Tag == "latest":
		return ref, corev1.PullAlways
	default:
		return ref, corev1.PullIfNotPresent
	}
}

// buildExporterContainer builds the metrics exporter sidecar. It inherits the
// backup agent's environment (restic repository + credentials) so it can query
// the same repository, then applies the listen port and exporter-specific env.
func buildExporterContainer(exporter *v1alpha1.Exporter, agentEnv []corev1.EnvVar) corev1.Container {
	port := exporter.Port
	if port == 0 {
		port = 8001
	}

	image, pullPolicy := resolveImage(exporter.Image)

	env := append([]corev1.EnvVar{}, agentEnv...)
	env = append(env, corev1.EnvVar{Name: "LISTEN_PORT", Value: fmt.Sprintf("%d", port)})
	env = append(env, exporter.Environment...)

	liveness := exporter.LivenessProbe
	if liveness == nil {
		liveness = metricsProbe(port, 30, 60, 5)
	}
	readiness := exporter.ReadinessProbe
	if readiness == nil {
		readiness = metricsProbe(port, 10, 30, 3)
	}

	return corev1.Container{
		Name:            "restic-exporter",
		Image:           image,
		ImagePullPolicy: pullPolicy,
		Env:             env,
		Ports: []corev1.ContainerPort{{
			Name:          "metrics",
			ContainerPort: port,
			Protocol:      corev1.ProtocolTCP,
		}},
		LivenessProbe:  liveness,
		ReadinessProbe: readiness,
		StartupProbe:   exporter.StartupProbe,
	}
}

// metricsProbe builds an HTTP GET probe against /metrics on the given port,
// used as the default health check for the exporter sidecar.
func metricsProbe(port, initialDelaySeconds, periodSeconds, failureThreshold int32) *corev1.Probe {
	return &corev1.Probe{
		ProbeHandler: corev1.ProbeHandler{
			HTTPGet: &corev1.HTTPGetAction{
				Path: "/metrics",
				Port: intstr.FromInt32(port),
			},
		},
		InitialDelaySeconds: initialDelaySeconds,
		PeriodSeconds:       periodSeconds,
		TimeoutSeconds:      5,
		FailureThreshold:    failureThreshold,
	}
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

func (d *PodCustomDefaulter) getDetectedVolumeMounts(pod corev1.Pod) ([]corev1.VolumeMount, error) {
	annotations := pod.GetAnnotations()
	volumeAnnotation := annotations[constants.AutoDetectVolumeAnnotation]
	containerAnnotation := annotations[constants.AutoDetectContainerAnnotation]

	var validVolumes []string
	for _, v := range pod.Spec.Volumes {
		valid := false

		if volumeAnnotation != "" {
			if v.Name == volumeAnnotation {
				valid = true
			}
		} else {
			if v.HostPath != nil && v.HostPath.Path != "" {
				valid = true
			}

			if v.PersistentVolumeClaim != nil && v.PersistentVolumeClaim.ClaimName != "" {
				valid = true
			}

			if v.NFS != nil && v.NFS.Server != "" {
				valid = true
			}

			if v.ISCSI != nil && v.ISCSI.IQN != "" {
				valid = true
			}

			if v.FC != nil && v.FC.Lun != nil {
				valid = true
			}

			if v.CSI != nil && v.CSI.Driver != "" {
				valid = true
			}
		}

		if valid {
			if !slices.Contains(validVolumes, v.Name) {
				validVolumes = append(validVolumes, v.Name)
			}
		}
	}

	var mounts []corev1.VolumeMount
	for _, c := range pod.Spec.Containers {
		if containerAnnotation != "" && c.Name != containerAnnotation {
			continue
		}

		for _, m := range c.VolumeMounts {
			if slices.Contains(validVolumes, m.Name) {
				mounts = append(mounts, m)
			}
		}
	}

	if len(mounts) == 0 {
		return nil, fmt.Errorf("no volume found")
	}

	return mounts, nil
}

func (d *PodCustomDefaulter) applyRetentionDays(annotations map[string]string, container *corev1.Container) error {
	retentionDaysStr, ok := annotations[constants.RetentionDaysAnnotation]
	if !ok {
		return nil
	}

	retentionDays, err := strconv.Atoi(retentionDaysStr)
	if err != nil || retentionDays <= 0 {
		return fmt.Errorf("annotation %q must be a positive integer, got %q", constants.RetentionDaysAnnotation, retentionDaysStr)
	}

	container.Env = append(container.Env, corev1.EnvVar{
		Name:  "BC_RETENTION_DAYS",
		Value: retentionDaysStr,
	})

	return nil
}
