package v1alpha1

import (
	"context"
	"fmt"

	"github.com/rclsilver-org/backup-controller/internal/constants"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// +kubebuilder:rbac:groups=core,resources=pods,verbs=get;list;watch

// getMutatedPods returns the list of the mutated pods
func getMutatedPods(ctx context.Context, c client.Client) ([]corev1.Pod, error) {
	var result corev1.PodList

	if err := c.List(ctx, &result, client.MatchingLabels{constants.MutatedLabel: "true"}); err != nil {
		return nil, fmt.Errorf("error while fetching mutated pods: %w", err)

	}

	return result.Items, nil
}
