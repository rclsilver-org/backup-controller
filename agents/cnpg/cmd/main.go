package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/rclsilver-org/backup-controller/agents/common"
	"github.com/rclsilver-org/backup-controller/agents/default/outputs"
)

const (
	MY_POD_NAME  = "MY_POD_NAME"
	MY_NAMESPACE = "MY_NAMESPACE"

	backupPhaseCompleted = "completed"
	backupPhaseFailed    = "failed"
)

var (
	scheduledBackupGVR = schema.GroupVersionResource{
		Group:    "postgresql.cnpg.io",
		Version:  "v1",
		Resource: "scheduledbackups",
	}

	backupGVR = schema.GroupVersionResource{
		Group:    "postgresql.cnpg.io",
		Version:  "v1",
		Resource: "backups",
	}
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	logLevel := slog.LevelInfo
	if common.IsDebug() {
		logLevel = slog.LevelDebug
	}

	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: logLevel,
	})))

	slog.DebugContext(ctx, "starting the CNPG backup agent")

	// Check environment variables
	if err := common.RequiredEnvVar(MY_POD_NAME, MY_NAMESPACE); err != nil {
		slog.ErrorContext(ctx, "unable to verify environment variables", "error", err)
		os.Exit(1)
	}
	myPodName := os.Getenv(MY_POD_NAME)
	myNamespace := os.Getenv(MY_NAMESPACE)

	// Init the output module
	if err := outputs.Init(ctx); err != nil {
		slog.ErrorContext(ctx, "unable to initialize the output module", "error", err)
		os.Exit(1)
	}

	// Create Kubernetes clients
	clientset, dynamicClient, err := getKubernetesClient()
	if err != nil {
		slog.ErrorContext(ctx, "failed to create Kubernetes client", "error", err)
		outputs.SetUnknown(ctx, fmt.Errorf("failed to create Kubernetes client: %w", err))
		os.Exit(1)
	}
	slog.DebugContext(ctx, "successfully connected to Kubernetes cluster")

	// Get server version to verify connection (note: ServerVersion() doesn't support context)
	// For future API calls, always use methods that accept context.Context as first parameter
	version, err := clientset.Discovery().ServerVersion()
	if err != nil {
		slog.ErrorContext(ctx, "failed to get Kubernetes server version", "error", err)
		outputs.SetUnknown(ctx, fmt.Errorf("failed to get Kubernetes server version: %w", err))
		os.Exit(1)
	}
	slog.DebugContext(ctx, "connected to Kubernetes cluster", "version", version.String())

	clusterName, err := getClusterName(ctx, clientset, myPodName, myNamespace)
	if err != nil {
		slog.ErrorContext(ctx, "failed to get CNPG cluster information", "error", err)
		outputs.SetUnknown(ctx, fmt.Errorf("failed to get CNPG cluster information: %w", err))
		os.Exit(1)
	}

	// Start watching scheduled backups dynamically with retry logic
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		// Retry loop with exponential backoff
		initialBackoff := time.Second
		backoff := initialBackoff
		maxBackoff := 5 * time.Minute

		for {
			select {
			case <-ctx.Done():
				slog.DebugContext(ctx, "context cancelled, stopping retry loop")
				return
			default:
			}

			watchStart := time.Now()
			if err := watchScheduledBackups(ctx, dynamicClient, clusterName, myNamespace); err != nil {
				if ctx.Err() != nil {
					// Context was cancelled, exit gracefully
					return
				}

				// Reset backoff if the previous watch ran long enough to be considered stable
				if time.Since(watchStart) > backoff {
					backoff = initialBackoff
				}

				slog.ErrorContext(ctx, "error watching scheduled backups, retrying", "error", err, "backoff", backoff)
				outputs.SetUnknown(ctx, fmt.Errorf("temporarily unable to watch backups: %w", err))

				timer := time.NewTimer(backoff)
				select {
				case <-timer.C:
					backoff *= 2
					if backoff > maxBackoff {
						backoff = maxBackoff
					}
				case <-ctx.Done():
					timer.Stop()
					return
				}
			} else {
				// Successful completion (context cancelled), exit
				return
			}
		}
	}()

	// Wait for shutdown signal
	<-ctx.Done()
	slog.DebugContext(ctx, "shutdown signal received, waiting for all watchers to stop...")

	// Wait for all goroutines to finish
	wg.Wait()
	slog.DebugContext(ctx, "all watchers stopped, shutting down the CNPG backup agent")
}

func getKubernetesClient() (*kubernetes.Clientset, dynamic.Interface, error) {
	// Try in-cluster configuration first
	config, err := rest.InClusterConfig()
	if err != nil {
		// Fall back to kubeconfig
		loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
		configOverrides := &clientcmd.ConfigOverrides{}
		kubeConfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, configOverrides)
		config, err = kubeConfig.ClientConfig()
		if err != nil {
			return nil, nil, err
		}
	}

	// Create the clientset
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, nil, err
	}

	// Create the dynamic client
	dynamicClient, err := dynamic.NewForConfig(config)
	if err != nil {
		return nil, nil, err
	}

	return clientset, dynamicClient, nil
}

func getClusterName(ctx context.Context, clientset *kubernetes.Clientset, podName, namespace string) (string, error) {
	pod, err := clientset.CoreV1().Pods(namespace).Get(ctx, podName, metav1.GetOptions{})
	if err != nil {
		return "", err
	}

	if len(pod.OwnerReferences) == 0 {
		return "", fmt.Errorf("pod %q in namespace %q does not have any owner references", podName, namespace)
	}
	if len(pod.OwnerReferences) > 1 {
		return "", fmt.Errorf("pod %q in namespace %q has multiple owner references, unable to determine the CNPG cluster", podName, namespace)
	}

	ownerRef := pod.OwnerReferences[0]
	if ownerRef.Kind != "Cluster" {
		return "", fmt.Errorf("pod %q in namespace %q is owned by a %q, not a Cluster", podName, namespace, ownerRef.Kind)
	}

	return ownerRef.Name, nil
}

// watchScheduledBackups watches for ScheduledBackup resources and manages backup watchers dynamically
func watchScheduledBackups(ctx context.Context, dynamicClient dynamic.Interface, clusterName, namespace string) error {
	slog.DebugContext(ctx, "starting to watch scheduled backups", "clusterName", clusterName)

	// Map to track active backup watchers: scheduledBackupName -> cancel function
	activeWatchers := make(map[string]context.CancelFunc)
	var watchersMutex sync.Mutex
	var watchersWg sync.WaitGroup

	// Cleanup function to stop all watchers
	defer func() {
		watchersMutex.Lock()
		for sbName, cancel := range activeWatchers {
			slog.DebugContext(ctx, "cleaning up backup watcher", "scheduledBackup", sbName)
			cancel()
		}
		activeWatchers = make(map[string]context.CancelFunc)
		watchersMutex.Unlock()
		watchersWg.Wait()
	}()

	// Helper function to start a backup watcher
	startBackupWatcher := func(scheduledBackupName string) {
		watchersMutex.Lock()
		// Check if already watching
		if _, exists := activeWatchers[scheduledBackupName]; exists {
			watchersMutex.Unlock()
			return
		}

		// Create a cancellable context for this watcher
		watcherCtx, cancel := context.WithCancel(ctx)
		activeWatchers[scheduledBackupName] = cancel
		watchersMutex.Unlock()

		watchersWg.Add(1)
		go func(sbName string) {
			defer watchersWg.Done()
			defer func() {
				// Clean up the watcher from activeWatchers when it stops
				watchersMutex.Lock()
				delete(activeWatchers, sbName)
				watchersMutex.Unlock()
			}()

			watchBackupsForScheduledBackup(watcherCtx, dynamicClient, sbName, namespace)
			slog.DebugContext(ctx, "backup watcher stopped", "scheduledBackup", sbName)
		}(scheduledBackupName)

		slog.DebugContext(ctx, "started backup watcher", "scheduledBackup", scheduledBackupName)
	}

	// Helper function to stop a backup watcher
	stopBackupWatcher := func(scheduledBackupName string) {
		watchersMutex.Lock()
		cancel, exists := activeWatchers[scheduledBackupName]
		if exists {
			cancel()
			delete(activeWatchers, scheduledBackupName)
		}
		watchersMutex.Unlock()

		if exists {
			slog.DebugContext(ctx, "stopped backup watcher", "scheduledBackup", scheduledBackupName)
		}
	}

	// First, list existing scheduled backups and start watchers for them
	list, err := dynamicClient.Resource(scheduledBackupGVR).Namespace(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("failed to list scheduled backups: %w", err)
	}

	for _, item := range list.Items {
		cluster, found, err := unstructured.NestedString(item.Object, "spec", "cluster", "name")
		if err != nil || !found || cluster != clusterName {
			continue
		}
		startBackupWatcher(item.GetName())
	}

	// Start watching from the current resource version
	resourceVersion := list.GetResourceVersion()
	watcher, err := dynamicClient.Resource(scheduledBackupGVR).Namespace(namespace).Watch(ctx, metav1.ListOptions{
		ResourceVersion: resourceVersion,
	})
	if err != nil {
		return fmt.Errorf("failed to watch scheduled backups: %w", err)
	}
	defer watcher.Stop()

	for {
		select {
		case <-ctx.Done():
			slog.DebugContext(ctx, "stopping watch for scheduled backups")
			// Cleanup is handled by defer
			return nil

		case event, ok := <-watcher.ResultChan():
			if !ok {
				// Watcher closed, need to restart
				slog.DebugContext(ctx, "scheduled backup watcher closed, will retry")
				// Return error to trigger retry with backoff
				return fmt.Errorf("watcher channel closed")
			}

			scheduledBackup, ok := event.Object.(*unstructured.Unstructured)
			if !ok {
				continue
			}

			// Check if this scheduled backup belongs to our cluster
			cluster, found, err := unstructured.NestedString(scheduledBackup.Object, "spec", "cluster", "name")
			if err != nil || !found || cluster != clusterName {
				continue
			}

			scheduledBackupName := scheduledBackup.GetName()

			switch event.Type {
			case watch.Added:
				slog.InfoContext(ctx, "new scheduled backup detected", "scheduledBackup", scheduledBackupName)
				startBackupWatcher(scheduledBackupName)

			case watch.Deleted:
				slog.InfoContext(ctx, "scheduled backup deleted", "scheduledBackup", scheduledBackupName)
				stopBackupWatcher(scheduledBackupName)

			case watch.Modified:
				// Check if cluster name changed
				if cluster != clusterName {
					slog.InfoContext(ctx, "scheduled backup no longer belongs to this cluster", "scheduledBackup", scheduledBackupName)
					stopBackupWatcher(scheduledBackupName)
				}
			}
		}
	}
}

// watchBackupsForScheduledBackup watches for Backup resources owned by the given ScheduledBackup
func watchBackupsForScheduledBackup(ctx context.Context, dynamicClient dynamic.Interface, scheduledBackupName, namespace string) {
	slog.DebugContext(ctx, "starting to watch backups", "scheduledBackup", scheduledBackupName)

	// Retry loop with exponential backoff
	initialBackoff := time.Second
	backoff := initialBackoff
	maxBackoff := time.Minute

	for {
		select {
		case <-ctx.Done():
			slog.DebugContext(ctx, "context cancelled, stopping backup watcher", "scheduledBackup", scheduledBackupName)
			return
		default:
		}

		watchStart := time.Now()
		err := watchBackupsForScheduledBackupOnce(ctx, dynamicClient, scheduledBackupName, namespace)
		if err != nil {
			if ctx.Err() != nil {
				// Context was cancelled, exit gracefully
				return
			}

			// Reset backoff if the previous watch ran long enough to be considered stable
			if time.Since(watchStart) > backoff {
				backoff = initialBackoff
			}

			slog.ErrorContext(ctx, "error watching backups, retrying", "scheduledBackup", scheduledBackupName, "error", err, "backoff", backoff)

			timer := time.NewTimer(backoff)
			select {
			case <-timer.C:
				backoff *= 2
				if backoff > maxBackoff {
					backoff = maxBackoff
				}
			case <-ctx.Done():
				timer.Stop()
				return
			}
		} else {
			// Successful completion (context cancelled)
			return
		}
	}
}

// watchBackupsForScheduledBackupOnce performs a single watch cycle
func watchBackupsForScheduledBackupOnce(ctx context.Context, dynamicClient dynamic.Interface, scheduledBackupName, namespace string) error {

	// First, list existing backups to get the current ResourceVersion
	list, err := dynamicClient.Resource(backupGVR).Namespace(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("failed to list backups: %w", err)
	}

	mostRecentBackup, mostRecentBackupTime := findMostRecentBackup(list, scheduledBackupName)
	lastPhase, lastErrMsg := reportInitialBackupStatus(ctx, scheduledBackupName, mostRecentBackup, mostRecentBackupTime)

	// Start watching from the current ResourceVersion to only get new events
	resourceVersion := list.GetResourceVersion()
	slog.DebugContext(ctx, "starting watch from resource version", "scheduledBackup", scheduledBackupName, "resourceVersion", resourceVersion)

	// Watch backups in the namespace starting from the current resource version
	watcher, err := dynamicClient.Resource(backupGVR).Namespace(namespace).Watch(ctx, metav1.ListOptions{
		ResourceVersion: resourceVersion,
	})
	if err != nil {
		return fmt.Errorf("failed to watch backups: %w", err)
	}
	defer watcher.Stop()

	// Track which backups we're currently monitoring
	trackedBackups := make(map[string]bool)

	// Setup heartbeat ticker to send periodic status updates to Icinga
	// This ensures Icinga knows the monitoring is still active even if no backups occur
	heartbeatInterval := 1 * time.Hour // Send heartbeat every hour
	heartbeatTicker := time.NewTicker(heartbeatInterval)
	defer heartbeatTicker.Stop()

	lastHeartbeat := time.Now()
	// Keep track of the last backup time for heartbeat messages
	var lastBackupTime time.Time
	if mostRecentBackup != nil {
		lastBackupTime = mostRecentBackupTime
	}

	for {
		select {
		case <-ctx.Done():
			slog.DebugContext(ctx, "stopping watch for backups", "scheduledBackup", scheduledBackupName)
			return nil

		case <-heartbeatTicker.C:
			lastHeartbeat = sendHeartbeat(ctx, scheduledBackupName, lastHeartbeat, lastBackupTime, lastPhase, lastErrMsg)

		case event, ok := <-watcher.ResultChan():
			if !ok {
				// Watcher closed, return error to trigger retry
				slog.DebugContext(ctx, "backup watcher channel closed", "scheduledBackup", scheduledBackupName)
				return fmt.Errorf("watcher channel closed")
			}

			backup, ok := event.Object.(*unstructured.Unstructured)
			if !ok {
				continue
			}

			// Check if this backup is owned by our ScheduledBackup
			if !isOwnedBy(backup, scheduledBackupName, "ScheduledBackup") {
				continue
			}

			backupName := backup.GetName()

			switch event.Type {
			case watch.Added, watch.Modified:
				finished, phase, errMsg, err := handleBackupEvent(ctx, scheduledBackupName, backupName, backup, trackedBackups)
				if err != nil {
					continue
				}
				if !finished.IsZero() {
					lastBackupTime = finished
					lastPhase = phase
					lastErrMsg = errMsg
					heartbeatTicker.Reset(heartbeatInterval)
					lastHeartbeat = time.Now()
					delete(trackedBackups, backupName)
				}

			case watch.Deleted:
				slog.InfoContext(ctx, "backup deleted", "scheduledBackup", scheduledBackupName, "backup", backupName)
				delete(trackedBackups, backupName)
			}
		}
	}
}

// findMostRecentBackup returns the most recently completed or failed backup owned by scheduledBackupName.
func findMostRecentBackup(list *unstructured.UnstructuredList, scheduledBackupName string) (*unstructured.Unstructured, time.Time) {
	var mostRecent *unstructured.Unstructured
	var mostRecentTime time.Time

	for _, item := range list.Items {
		if !isOwnedBy(&item, scheduledBackupName, "ScheduledBackup") {
			continue
		}

		phase, found, err := unstructured.NestedString(item.Object, "status", "phase")
		if err != nil || !found {
			continue
		}

		if phase != backupPhaseCompleted && phase != backupPhaseFailed {
			continue
		}

		stoppedAtStr, found, err := unstructured.NestedString(item.Object, "status", "stoppedAt")
		if err != nil || !found {
			continue
		}

		stoppedAt, err := time.Parse(time.RFC3339, stoppedAtStr)
		if err != nil {
			continue
		}

		if mostRecent == nil || stoppedAt.After(mostRecentTime) {
			itemCopy := item.DeepCopy()
			mostRecent = itemCopy
			mostRecentTime = stoppedAt
		}
	}

	return mostRecent, mostRecentTime
}

// reportInitialBackupStatus sends the current backup status to Icinga at startup or after a retry.
// Returns the phase and error message of the reported backup (empty strings if none found).
func reportInitialBackupStatus(ctx context.Context, scheduledBackupName string, mostRecentBackup *unstructured.Unstructured, mostRecentBackupTime time.Time) (phase, errMsg string) {
	if mostRecentBackup == nil {
		slog.InfoContext(ctx, "no backup found at startup", "scheduledBackup", scheduledBackupName)
		outputs.SetUnknown(ctx, fmt.Errorf("monitoring started, no backup executed yet"))
		return "", ""
	}

	backupName := mostRecentBackup.GetName()
	var found bool
	phase, found, _ = unstructured.NestedString(mostRecentBackup.Object, "status", "phase")
	if !found || (phase != backupPhaseCompleted && phase != backupPhaseFailed) {
		slog.WarnContext(ctx, "most recent backup has unrecognized phase at startup", "scheduledBackup", scheduledBackupName, "backup", backupName, "phase", phase)
		outputs.SetUnknown(ctx, fmt.Errorf("most recent backup %q has unrecognized phase %q", backupName, phase))
		return "", ""
	}

	timeSinceExecution := time.Since(mostRecentBackupTime)

	if phase == backupPhaseCompleted {
		startedAtStr, _, _ := unstructured.NestedString(mostRecentBackup.Object, "status", "startedAt")
		startedAt, parseErr := time.Parse(time.RFC3339, startedAtStr)

		slog.InfoContext(ctx, "found most recent backup at startup",
			"scheduledBackup", scheduledBackupName,
			"backup", backupName,
			"phase", phase,
			"completedAt", mostRecentBackupTime,
			"timeAgo", timeSinceExecution)

		perfData := map[string]any{
			"executed_at":          mostRecentBackupTime.Unix(),
			"time_since_execution": timeSinceExecution.Seconds(),
		}
		var msg string
		if parseErr == nil {
			duration := mostRecentBackupTime.Sub(startedAt)
			perfData["duration"] = duration.Seconds()
			msg = fmt.Sprintf("last backup executed %s ago (completed successfully in %s)", formatDuration(timeSinceExecution), duration.Round(time.Second))
		} else {
			msg = fmt.Sprintf("last backup executed %s ago (completed successfully)", formatDuration(timeSinceExecution))
		}
		outputs.SetSuccess(ctx, msg, perfData)
		return phase, ""
	}

	// phase == backupPhaseFailed
	errMsg, found, _ = unstructured.NestedString(mostRecentBackup.Object, "status", "error")
	if !found || errMsg == "" {
		errMsg = "unknown error"
	}

	slog.WarnContext(ctx, "found most recent backup at startup (failed)",
		"scheduledBackup", scheduledBackupName,
		"backup", backupName,
		"phase", phase,
		"failedAt", mostRecentBackupTime,
		"timeAgo", timeSinceExecution,
		"error", errMsg)

	outputs.SetError(ctx, fmt.Errorf("last backup executed %s ago (failed: %s)", formatDuration(timeSinceExecution), errMsg))
	return phase, errMsg
}

// sendHeartbeat sends a periodic status update to Icinga, re-emitting the last known backup state.
func sendHeartbeat(ctx context.Context, scheduledBackupName string, lastHeartbeat time.Time, lastBackupTime time.Time, lastPhase, lastErrMsg string) time.Time {
	timeSinceLastHeartbeat := time.Since(lastHeartbeat)

	slog.DebugContext(ctx, "sending heartbeat to Icinga", "scheduledBackup", scheduledBackupName, "lastPhase", lastPhase, "timeSinceLastHeartbeat", timeSinceLastHeartbeat)

	switch lastPhase {
	case backupPhaseCompleted:
		timeSinceLastBackup := time.Since(lastBackupTime)
		outputs.SetSuccess(ctx, fmt.Sprintf("last backup executed %s ago (completed successfully)", formatDuration(timeSinceLastBackup)), map[string]any{
			"executed_at":          lastBackupTime.Unix(),
			"time_since_execution": timeSinceLastBackup.Seconds(),
		})
	case backupPhaseFailed:
		timeSinceLastBackup := time.Since(lastBackupTime)
		outputs.SetError(ctx, fmt.Errorf("last backup executed %s ago (failed: %s)", formatDuration(timeSinceLastBackup), lastErrMsg))
	default:
		outputs.SetUnknown(ctx, fmt.Errorf("monitoring active, no backup executed yet"))
	}

	return time.Now()
}

// handleBackupEvent processes an Added or Modified backup event.
// Returns the backup completion time (non-zero), phase, errMsg if the backup finished, and any processing error.
func handleBackupEvent(ctx context.Context, scheduledBackupName, backupName string, backup *unstructured.Unstructured, trackedBackups map[string]bool) (time.Time, string, string, error) {
	phase, found, err := unstructured.NestedString(backup.Object, "status", "phase")
	if err != nil {
		slog.ErrorContext(ctx, "failed to get backup phase", "backup", backupName, "error", err)
		return time.Time{}, "", "", err
	}

	if !found {
		if !trackedBackups[backupName] {
			slog.InfoContext(ctx, "new backup detected", "scheduledBackup", scheduledBackupName, "backup", backupName)
			trackedBackups[backupName] = true
		}
		return time.Time{}, "", "", nil
	}

	if !trackedBackups[backupName] {
		slog.InfoContext(ctx, "tracking backup", "scheduledBackup", scheduledBackupName, "backup", backupName, "phase", phase)
		trackedBackups[backupName] = true
	} else {
		slog.InfoContext(ctx, "backup status update", "scheduledBackup", scheduledBackupName, "backup", backupName, "phase", phase)
	}

	if phase != backupPhaseCompleted && phase != backupPhaseFailed {
		return time.Time{}, "", "", nil
	}

	finishedAt, errMsg, err := handleFinishedBackup(ctx, scheduledBackupName, backupName, backup, phase)
	if err != nil {
		return time.Time{}, "", "", err
	}

	return finishedAt, phase, errMsg, nil
}

// handleFinishedBackup processes a completed or failed backup and reports to Icinga.
// Returns the backup stop time and the error message if failed.
func handleFinishedBackup(ctx context.Context, scheduledBackupName, backupName string, backup *unstructured.Unstructured, phase string) (time.Time, string, error) {
	if phase == backupPhaseCompleted {
		startedAtStr, _, err := unstructured.NestedString(backup.Object, "status", "startedAt")
		if err != nil {
			slog.ErrorContext(ctx, "failed to get backup startedAt", "backup", backupName, "error", err)
			return time.Time{}, "", err
		}

		stoppedAtStr, _, err := unstructured.NestedString(backup.Object, "status", "stoppedAt")
		if err != nil {
			slog.ErrorContext(ctx, "failed to get backup stoppedAt", "backup", backupName, "error", err)
			return time.Time{}, "", err
		}

		startedAt, err := time.Parse(time.RFC3339, startedAtStr)
		if err != nil {
			slog.ErrorContext(ctx, "failed to parse backup startedAt", "backup", backupName, "error", err)
			return time.Time{}, "", err
		}

		stoppedAt, err := time.Parse(time.RFC3339, stoppedAtStr)
		if err != nil {
			slog.ErrorContext(ctx, "failed to parse backup stoppedAt", "backup", backupName, "error", err)
			return time.Time{}, "", err
		}

		duration := stoppedAt.Sub(startedAt)
		slog.InfoContext(ctx, "backup completed", "scheduledBackup", scheduledBackupName, "backup", backupName)
		outputs.SetSuccess(ctx, fmt.Sprintf("backup process completed successfully in %s at %s", duration, stoppedAt), map[string]any{
			"duration": duration.Seconds(),
		})

		return stoppedAt, "", nil
	}

	// phase == backupPhaseFailed
	errorMsg, found, err := unstructured.NestedString(backup.Object, "status", "error")
	if err != nil {
		slog.ErrorContext(ctx, "failed to get backup error", "backup", backupName, "error", err)
		return time.Time{}, "", err
	}

	if !found {
		errorMsg = "unknown error"
	}

	slog.WarnContext(ctx, "backup failed", "scheduledBackup", scheduledBackupName, "backup", backupName, "error", errorMsg)
	outputs.SetError(ctx, fmt.Errorf("backup failed: %s", errorMsg))

	stoppedAtStr, _, _ := unstructured.NestedString(backup.Object, "status", "stoppedAt")
	if stoppedAtStr != "" {
		if stoppedAt, err := time.Parse(time.RFC3339, stoppedAtStr); err == nil {
			return stoppedAt, errorMsg, nil
		}
	}

	return time.Now(), errorMsg, nil
}

// isOwnedBy checks if the object is owned by a resource with the given name and kind
func isOwnedBy(obj *unstructured.Unstructured, ownerName, ownerKind string) bool {
	ownerRefs := obj.GetOwnerReferences()
	for _, ref := range ownerRefs {
		if ref.Kind == ownerKind && ref.Name == ownerName {
			return true
		}
	}
	return false
}

// formatDuration formats a duration in a human-readable format
func formatDuration(d time.Duration) string {
	d = d.Round(time.Second)

	days := d / (24 * time.Hour)
	d -= days * 24 * time.Hour

	hours := d / time.Hour
	d -= hours * time.Hour

	minutes := d / time.Minute
	d -= minutes * time.Minute

	seconds := d / time.Second

	if days > 0 {
		if hours > 0 {
			return fmt.Sprintf("%dd%dh", days, hours)
		}
		return fmt.Sprintf("%dd", days)
	}

	if hours > 0 {
		if minutes > 0 {
			return fmt.Sprintf("%dh%dm", hours, minutes)
		}
		return fmt.Sprintf("%dh", hours)
	}

	if minutes > 0 {
		if seconds > 0 {
			return fmt.Sprintf("%dm%ds", minutes, seconds)
		}
		return fmt.Sprintf("%dm", minutes)
	}

	return fmt.Sprintf("%ds", seconds)
}
