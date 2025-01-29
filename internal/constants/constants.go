package constants

const (
	// PolicyAnnotation is the annotation used by the user to define the policy to use
	PolicyAnnotation = "backup-controller.rclsilver-org.github.com/policy"

	// ScheduleAnnotation is the annotation used by the user to define the schedule to use
	ScheduleAnnotation = "backup-controller.rclsilver-org.github.com/schedule"

	// MutatedLabel is the label set by the controller when a pod is mutated
	MutatedLabel = "backup-controller.rclsilver-org.github.com/mutated"
)
