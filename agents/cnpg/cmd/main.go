package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/robfig/cron/v3"
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

	// defaultBackupInterval is used when the ScheduledBackup schedule is missing
	// or cannot be parsed.
	defaultBackupInterval = 24 * time.Hour

	// backupGracePeriod is added to the schedule interval before a missing or
	// in-progress backup is considered overdue. It leaves room for a backup to
	// actually run after its scheduled time and for brief cluster unavailability.
	// Tune it up if backups legitimately run long.
	backupGracePeriod = 2 * time.Hour

	// backupHeartbeatInterval is how often the health is re-evaluated when no
	// watch event arrives, so overdue/stuck backups are detected in the absence
	// of any status change.
	backupHeartbeatInterval = 15 * time.Minute
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

	clusterGVR = schema.GroupVersionResource{
		Group:    "postgresql.cnpg.io",
		Version:  "v1",
		Resource: "clusters",
	}
)

// Only the agent on the primary instance reports to the output, so an HA
// (multi-instance) cluster doesn't push the same passive check from every replica
// and flap when a replica restarts. isPrimary is kept live by watchClusterPrimary
// (follows failover); reReport nudges the backup watchers to push immediately when
// the role flips.
var (
	isPrimary atomic.Bool
	reReport  = make(chan struct{}, 1)
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

	// Seed the primary role before starting the reporters, then keep it live.
	if err := refreshPrimary(ctx, dynamicClient, clusterName, myNamespace, myPodName); err != nil {
		slog.WarnContext(ctx, "unable to determine initial primary role, assuming replica", "error", err)
	}

	var wg sync.WaitGroup

	// Track the primary role live (follows failover) so only the primary reports.
	wg.Add(1)
	go func() {
		defer wg.Done()
		watchClusterPrimary(ctx, dynamicClient, clusterName, myNamespace, myPodName)
	}()

	// Start watching scheduled backups dynamically with retry logic
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

				// A closed watch channel is normal (the API server rotates watches):
				// retry silently like watchClusterPrimary rather than flapping the
				// Icinga status to UNKNOWN. The real backup status is re-reported once
				// the watch re-establishes and by the hourly sensor.
				slog.ErrorContext(ctx, "error watching scheduled backups, retrying", "error", err, "backoff", backoff)

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

// refreshPrimary reads the cluster's current primary once and updates isPrimary.
func refreshPrimary(ctx context.Context, dynamicClient dynamic.Interface, clusterName, namespace, myPodName string) error {
	cluster, err := dynamicClient.Resource(clusterGVR).Namespace(namespace).Get(ctx, clusterName, metav1.GetOptions{})
	if err != nil {
		return err
	}
	setPrimary(ctx, cluster, myPodName)
	return nil
}

// setPrimary updates isPrimary from a Cluster object. On a role change it logs the
// transition (promotion/demotion) at INFO and nudges the reporters, so the new
// primary pushes immediately on failover and the old one stops.
func setPrimary(ctx context.Context, cluster *unstructured.Unstructured, myPodName string) {
	currentPrimary, _, _ := unstructured.NestedString(cluster.Object, "status", "currentPrimary")
	newVal := currentPrimary != "" && currentPrimary == myPodName

	old := isPrimary.Swap(newVal)
	if old == newVal {
		return
	}

	if newVal {
		slog.InfoContext(ctx, "instance PROMOTED to primary — this agent will now report backup status to the output", "pod", myPodName, "currentPrimary", currentPrimary)
	} else {
		slog.InfoContext(ctx, "instance DEMOTED to replica — this agent stops reporting backup status (the primary takes over)", "pod", myPodName, "currentPrimary", currentPrimary)
	}

	// Nudge the backup watchers to re-report immediately (non-blocking).
	select {
	case reReport <- struct{}{}:
	default:
	}
}

// watchClusterPrimary keeps isPrimary in sync with the cluster's currentPrimary,
// retrying the watch with exponential backoff so role changes (failover) are
// followed in near real time rather than at the backup heartbeat cadence.
func watchClusterPrimary(ctx context.Context, dynamicClient dynamic.Interface, clusterName, namespace, myPodName string) {
	initialBackoff := time.Second
	backoff := initialBackoff
	maxBackoff := time.Minute

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		watchStart := time.Now()
		if err := watchClusterPrimaryOnce(ctx, dynamicClient, clusterName, namespace, myPodName); err != nil {
			if ctx.Err() != nil {
				return
			}
			if time.Since(watchStart) > backoff {
				backoff = initialBackoff
			}
			slog.ErrorContext(ctx, "error watching cluster primary, retrying", "error", err, "backoff", backoff)
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
			return
		}
	}
}

func watchClusterPrimaryOnce(ctx context.Context, dynamicClient dynamic.Interface, clusterName, namespace, myPodName string) error {
	// Re-read + apply current state, and get the ResourceVersion to watch from.
	cluster, err := dynamicClient.Resource(clusterGVR).Namespace(namespace).Get(ctx, clusterName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get cluster %q: %w", clusterName, err)
	}
	setPrimary(ctx, cluster, myPodName)

	watcher, err := dynamicClient.Resource(clusterGVR).Namespace(namespace).Watch(ctx, metav1.ListOptions{
		ResourceVersion: cluster.GetResourceVersion(),
	})
	if err != nil {
		return fmt.Errorf("failed to watch clusters: %w", err)
	}
	defer watcher.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil

		case event, ok := <-watcher.ResultChan():
			if !ok {
				return fmt.Errorf("cluster watcher channel closed")
			}
			cluster, ok := event.Object.(*unstructured.Unstructured)
			if !ok || cluster.GetName() != clusterName {
				continue
			}
			setPrimary(ctx, cluster, myPodName)
		}
	}
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

// watchBackupsForScheduledBackupOnce performs a single watch cycle. It re-lists
// and re-evaluates the real backup health at startup, on every backup event, and
// on a periodic heartbeat, so overdue or stuck backups are always detected even
// when no status change ever arrives.
func watchBackupsForScheduledBackupOnce(ctx context.Context, dynamicClient dynamic.Interface, scheduledBackupName, namespace string) error {
	interval := scheduleInterval(ctx, dynamicClient, scheduledBackupName, namespace)
	slog.DebugContext(ctx, "using backup schedule interval", "scheduledBackup", scheduledBackupName, "interval", interval)

	// reconcile lists every backup owned by the ScheduledBackup, derives the
	// health from the real cluster state, and reports it to the output. It
	// returns the list ResourceVersion so the watch can resume from it.
	reconcile := func() (string, error) {
		list, err := dynamicClient.Resource(backupGVR).Namespace(namespace).List(ctx, metav1.ListOptions{})
		if err != nil {
			return "", fmt.Errorf("failed to list backups: %w", err)
		}
		health := evaluateBackupHealth(list, scheduledBackupName, interval, time.Now())
		if isPrimary.Load() {
			reportHealth(ctx, scheduledBackupName, health)
		} else {
			slog.DebugContext(ctx, "not the primary instance, skipping backup health report", "scheduledBackup", scheduledBackupName)
		}
		return list.GetResourceVersion(), nil
	}

	// Initial evaluation, also giving us the ResourceVersion to watch from.
	resourceVersion, err := reconcile()
	if err != nil {
		return err
	}

	slog.DebugContext(ctx, "starting watch from resource version", "scheduledBackup", scheduledBackupName, "resourceVersion", resourceVersion)
	watcher, err := dynamicClient.Resource(backupGVR).Namespace(namespace).Watch(ctx, metav1.ListOptions{
		ResourceVersion: resourceVersion,
	})
	if err != nil {
		return fmt.Errorf("failed to watch backups: %w", err)
	}
	defer watcher.Stop()

	// The heartbeat re-evaluates periodically so an overdue or stuck backup is
	// surfaced even when no watch event ever fires.
	heartbeatTicker := time.NewTicker(backupHeartbeatInterval)
	defer heartbeatTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			slog.DebugContext(ctx, "stopping watch for backups", "scheduledBackup", scheduledBackupName)
			return nil

		case <-heartbeatTicker.C:
			if _, err := reconcile(); err != nil {
				slog.ErrorContext(ctx, "heartbeat reconcile failed", "scheduledBackup", scheduledBackupName, "error", err)
			}

		case <-reReport:
			// Primary role just flipped — re-evaluate and (if now primary) push now.
			if _, err := reconcile(); err != nil {
				slog.ErrorContext(ctx, "reconcile after primary change failed", "scheduledBackup", scheduledBackupName, "error", err)
			}

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

			// Only re-evaluate on events for backups owned by our ScheduledBackup.
			if !isOwnedBy(backup, scheduledBackupName, "ScheduledBackup") {
				continue
			}

			slog.DebugContext(ctx, "backup event, re-evaluating health", "scheduledBackup", scheduledBackupName, "backup", backup.GetName(), "type", event.Type)
			if _, err := reconcile(); err != nil {
				slog.ErrorContext(ctx, "reconcile after backup event failed", "scheduledBackup", scheduledBackupName, "error", err)
			}
		}
	}
}

// backupHealth is the outcome of evaluating the current backup state.
type backupHealth struct {
	level int // 0 OK, 1 WARNING, 2 CRITICAL, 3 UNKNOWN
	msg   string
	perf  map[string]any
}

const (
	healthOK       = 0
	healthWarning  = 1
	healthCritical = 2
	healthUnknown  = 3
)

// evaluateBackupHealth derives the monitoring status from the full list of
// backups owned by scheduledBackupName. Unlike the previous logic, it does not
// only look at the last completed/failed backup: it also detects backups stuck
// in a non-terminal phase (which block every subsequent scheduled backup) and
// successful backups that have grown too old relative to the schedule.
func evaluateBackupHealth(list *unstructured.UnstructuredList, scheduledBackupName string, interval time.Duration, now time.Time) backupHealth {
	maxAge := interval + backupGracePeriod

	var (
		lastCompleted     *unstructured.Unstructured
		lastCompletedTime time.Time
		lastTerminalTime  time.Time
		lastTerminalPhase string
		lastFailed        *unstructured.Unstructured
		oldestPending     *unstructured.Unstructured
		oldestPendingTime time.Time
		oldestPendingLbl  string
	)

	for i := range list.Items {
		item := &list.Items[i]
		if !isOwnedBy(item, scheduledBackupName, "ScheduledBackup") {
			continue
		}

		phase, _, _ := unstructured.NestedString(item.Object, "status", "phase")

		if phase == backupPhaseCompleted || phase == backupPhaseFailed {
			stoppedAtStr, found, err := unstructured.NestedString(item.Object, "status", "stoppedAt")
			if !found || err != nil {
				continue
			}
			stoppedAt, err := time.Parse(time.RFC3339, stoppedAtStr)
			if err != nil {
				continue
			}
			if stoppedAt.After(lastTerminalTime) {
				lastTerminalTime = stoppedAt
				lastTerminalPhase = phase
			}
			if phase == backupPhaseCompleted && (lastCompleted == nil || stoppedAt.After(lastCompletedTime)) {
				lastCompleted = item
				lastCompletedTime = stoppedAt
			}
			if phase == backupPhaseFailed && stoppedAt.Equal(lastTerminalTime) {
				lastFailed = item
			}
			continue
		}

		// Non-terminal: running/started, or empty status (queued/pending because
		// a previous backup is still holding the per-cluster backup lock). Track
		// the oldest such backup, as that is the one blocking the queue.
		startedAt := backupStartTime(item)
		if startedAt.IsZero() {
			continue
		}
		if oldestPending == nil || startedAt.Before(oldestPendingTime) {
			oldestPending = item
			oldestPendingTime = startedAt
			if phase == "" {
				oldestPendingLbl = "pending"
			} else {
				oldestPendingLbl = phase
			}
		}
	}

	// 1. A backup stuck in a non-terminal phase past the deadline blocks every
	// subsequent scheduled backup: surface it as critical.
	if oldestPending != nil {
		if age := now.Sub(oldestPendingTime); age > maxAge {
			return backupHealth{
				level: healthCritical,
				msg: fmt.Sprintf("backup %q stuck in phase %q for %s (blocks scheduled backups)",
					oldestPending.GetName(), oldestPendingLbl, formatDuration(age)),
			}
		}
	}

	// 2. No completed or failed backup yet.
	if lastTerminalPhase == "" {
		if oldestPending != nil {
			return backupHealth{
				level: healthUnknown,
				msg:   fmt.Sprintf("no completed backup yet (backup %q in progress for %s)", oldestPending.GetName(), formatDuration(now.Sub(oldestPendingTime))),
			}
		}
		return backupHealth{level: healthUnknown, msg: "no backup executed yet"}
	}

	// 3. The most recent terminal backup failed.
	if lastTerminalPhase == backupPhaseFailed {
		errMsg := "unknown error"
		if lastFailed != nil {
			if e, found, _ := unstructured.NestedString(lastFailed.Object, "status", "error"); found && e != "" {
				errMsg = e
			}
		}
		return backupHealth{
			level: healthCritical,
			msg:   fmt.Sprintf("last backup failed %s ago: %s", formatDuration(now.Sub(lastTerminalTime)), errMsg),
		}
	}

	// 4. The most recent terminal backup completed — check it is still fresh.
	age := now.Sub(lastCompletedTime)
	perf := map[string]any{
		"executed_at":          lastCompletedTime.Unix(),
		"time_since_execution": age.Seconds(),
	}
	if age > maxAge {
		level := healthWarning
		if age > maxAge+interval {
			// More than one interval missed: escalate.
			level = healthCritical
		}
		return backupHealth{
			level: level,
			msg:   fmt.Sprintf("last successful backup was %s ago, over the %s schedule (+%s grace)", formatDuration(age), formatDuration(interval), formatDuration(backupGracePeriod)),
			perf:  perf,
		}
	}

	// Fresh successful backup.
	if startedAtStr, _, _ := unstructured.NestedString(lastCompleted.Object, "status", "startedAt"); startedAtStr != "" {
		if startedAt, err := time.Parse(time.RFC3339, startedAtStr); err == nil {
			duration := lastCompletedTime.Sub(startedAt)
			perf["duration"] = duration.Seconds()
			return backupHealth{
				level: healthOK,
				msg:   fmt.Sprintf("last backup executed %s ago (completed successfully in %s)", formatDuration(age), duration.Round(time.Second)),
				perf:  perf,
			}
		}
	}
	return backupHealth{
		level: healthOK,
		msg:   fmt.Sprintf("last backup executed %s ago (completed successfully)", formatDuration(age)),
		perf:  perf,
	}
}

// reportHealth pushes the evaluated health to the configured output.
func reportHealth(ctx context.Context, scheduledBackupName string, h backupHealth) {
	slog.DebugContext(ctx, "reporting backup health", "scheduledBackup", scheduledBackupName, "level", h.level, "message", h.msg)
	switch h.level {
	case healthOK:
		outputs.SetSuccess(ctx, h.msg, h.perf)
	case healthWarning:
		outputs.SetWarning(ctx, fmt.Errorf("%s", h.msg))
	case healthCritical:
		outputs.SetError(ctx, fmt.Errorf("%s", h.msg))
	default:
		outputs.SetUnknown(ctx, fmt.Errorf("%s", h.msg))
	}
}

// backupStartTime returns the moment a backup started, falling back to its
// creation timestamp when status.startedAt is not set yet (queued backups).
func backupStartTime(backup *unstructured.Unstructured) time.Time {
	if s, found, _ := unstructured.NestedString(backup.Object, "status", "startedAt"); found && s != "" {
		if t, err := time.Parse(time.RFC3339, s); err == nil {
			return t
		}
	}
	return backup.GetCreationTimestamp().Time
}

// scheduleInterval fetches the ScheduledBackup and derives the interval between
// two runs from its cron schedule, falling back to defaultBackupInterval.
func scheduleInterval(ctx context.Context, dynamicClient dynamic.Interface, scheduledBackupName, namespace string) time.Duration {
	sb, err := dynamicClient.Resource(scheduledBackupGVR).Namespace(namespace).Get(ctx, scheduledBackupName, metav1.GetOptions{})
	if err != nil {
		slog.WarnContext(ctx, "unable to fetch scheduled backup, using default interval", "scheduledBackup", scheduledBackupName, "error", err, "default", defaultBackupInterval)
		return defaultBackupInterval
	}

	schedule, found, _ := unstructured.NestedString(sb.Object, "spec", "schedule")
	if !found || schedule == "" {
		slog.WarnContext(ctx, "scheduled backup has no schedule, using default interval", "scheduledBackup", scheduledBackupName, "default", defaultBackupInterval)
		return defaultBackupInterval
	}

	interval, err := parseScheduleInterval(schedule)
	if err != nil {
		slog.WarnContext(ctx, "unable to parse backup schedule, using default interval", "scheduledBackup", scheduledBackupName, "schedule", schedule, "error", err, "default", defaultBackupInterval)
		return defaultBackupInterval
	}
	return interval
}

// parseScheduleInterval computes the interval between two consecutive runs of a
// CNPG backup schedule (a 6-field cron expression, seconds included).
func parseScheduleInterval(schedule string) (time.Duration, error) {
	parser := cron.NewParser(cron.Second | cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
	sched, err := parser.Parse(schedule)
	if err != nil {
		return 0, err
	}

	// Derive the interval from two consecutive fire times, using a fixed
	// reference so the result is deterministic.
	ref := time.Unix(0, 0).UTC()
	first := sched.Next(ref)
	second := sched.Next(first)
	interval := second.Sub(first)
	if interval <= 0 {
		return 0, fmt.Errorf("computed non-positive interval from schedule %q", schedule)
	}
	return interval, nil
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
