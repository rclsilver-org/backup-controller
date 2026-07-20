package constants

const (
	// PolicyAnnotation is the annotation used by the user to define the policy to use
	PolicyAnnotation = "backup-controller.rclsilver-org.github.com/policy"

	// ScheduleAnnotation is the annotation used by the user to define the schedule to use
	ScheduleAnnotation = "backup-controller.rclsilver-org.github.com/schedule"

	// FilterAnnotation is the annotation used by the user to filter the pod name
	FilterAnnotation = "backup-controller.rclsilver-org.github.com/filter"

	// AutoDetectVolumeAnnotation specify the volume name to use when auto-detect is enabled
	AutoDetectVolumeAnnotation = "backup-controller.rclsilver-org.github.com/detect.volume"

	// AutoDetectContainerAnnotation specify the volume container to use when auto-detect is enabled
	AutoDetectContainerAnnotation = "backup-controller.rclsilver-org.github.com/detect.container"

	// RetentionDaysAnnotation is the annotation used to override BC_RETENTION_DAYS for a specific pod
	RetentionDaysAnnotation = "backup-controller.rclsilver-org.github.com/retention-days"

	// MutatedLabel is the label set by the controller when a pod is mutated
	MutatedLabel = "backup-controller.rclsilver-org.github.com/mutated"

	// ExporterLabel is the label set by the controller when a metrics exporter
	// sidecar is injected. A single PodMonitor selecting this label can then
	// auto-discover every exporter across all namespaces.
	ExporterLabel = "backup-controller.rclsilver-org.github.com/exporter"
)
