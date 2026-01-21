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

	// Start watching scheduled backups dynamically
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := watchScheduledBackups(ctx, dynamicClient, clusterName, myNamespace); err != nil {
			slog.ErrorContext(ctx, "error watching scheduled backups", "error", err)
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
			if err := watchBackupsForScheduledBackup(watcherCtx, dynamicClient, sbName, namespace); err != nil {
				slog.ErrorContext(ctx, "error watching backups", "scheduledBackup", sbName, "error", err)
			}
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

			// Stop all active watchers
			watchersMutex.Lock()
			for sbName, cancel := range activeWatchers {
				slog.DebugContext(ctx, "stopping backup watcher", "scheduledBackup", sbName)
				cancel()
			}
			activeWatchers = make(map[string]context.CancelFunc)
			watchersMutex.Unlock()

			// Wait for all watchers to finish
			watchersWg.Wait()
			return nil

		case event, ok := <-watcher.ResultChan():
			if !ok {
				// Watcher closed, need to restart
				slog.DebugContext(ctx, "scheduled backup watcher closed, restarting")

				// Stop all watchers before restarting
				watchersMutex.Lock()
				for _, cancel := range activeWatchers {
					cancel()
				}
				activeWatchers = make(map[string]context.CancelFunc)
				watchersMutex.Unlock()
				watchersWg.Wait()

				return watchScheduledBackups(ctx, dynamicClient, clusterName, namespace)
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
func watchBackupsForScheduledBackup(ctx context.Context, dynamicClient dynamic.Interface, scheduledBackupName, namespace string) error {
	slog.DebugContext(ctx, "starting to watch backups", "scheduledBackup", scheduledBackupName)

	// First, list existing backups to get the current ResourceVersion
	// This allows us to watch only new backups created after this point
	list, err := dynamicClient.Resource(backupGVR).Namespace(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("failed to list backups: %w", err)
	}

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

	for {
		select {
		case <-ctx.Done():
			slog.DebugContext(ctx, "stopping watch for backups", "scheduledBackup", scheduledBackupName)
			return nil

		case event, ok := <-watcher.ResultChan():
			if !ok {
				// Watcher closed, need to restart
				slog.DebugContext(ctx, "backup watcher closed, restarting", "scheduledBackup", scheduledBackupName)
				return watchBackupsForScheduledBackup(ctx, dynamicClient, scheduledBackupName, namespace)
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
				// Check if backup is completed
				phase, found, err := unstructured.NestedString(backup.Object, "status", "phase")
				if err != nil {
					slog.ErrorContext(ctx, "failed to get backup phase", "backup", backupName, "error", err)
					continue
				}

				if !found {
					// No phase yet, backup just created
					if !trackedBackups[backupName] {
						slog.InfoContext(ctx, "new backup detected", "scheduledBackup", scheduledBackupName, "backup", backupName)
						trackedBackups[backupName] = true
					}
					continue
				}

				// Log the backup status
				if !trackedBackups[backupName] {
					slog.InfoContext(ctx, "tracking backup", "scheduledBackup", scheduledBackupName, "backup", backupName, "phase", phase)
					trackedBackups[backupName] = true
				} else {
					slog.InfoContext(ctx, "backup status update", "scheduledBackup", scheduledBackupName, "backup", backupName, "phase", phase)
				}

				// If the backup is completed or failed
				if phase == "completed" || phase == "failed" {
					if phase == "completed" {
						startedAtStr, _, err := unstructured.NestedString(backup.Object, "status", "startedAt")
						if err != nil {
							slog.ErrorContext(ctx, "failed to get backup startedAt", "backup", backupName, "error", err)
							continue
						}

						stoppedAtStr, _, err := unstructured.NestedString(backup.Object, "status", "stoppedAt")
						if err != nil {
							slog.ErrorContext(ctx, "failed to get backup stoppedAt", "backup", backupName, "error", err)
							continue
						}

						startedAt, err := time.Parse(time.RFC3339, startedAtStr)
						if err != nil {
							slog.ErrorContext(ctx, "failed to parse backup startedAt", "backup", backupName, "error", err)
							continue
						}

						stoppedAt, err := time.Parse(time.RFC3339, stoppedAtStr)
						if err != nil {
							slog.ErrorContext(ctx, "failed to parse backup stoppedAt", "backup", backupName, "error", err)
							continue
						}

						duration := stoppedAt.Sub(startedAt)

						slog.InfoContext(ctx, "backup completed", "scheduledBackup", scheduledBackupName, "backup", backupName)
						outputs.SetSuccess(ctx, fmt.Sprintf("backup process completed successfully in %s at %s", duration, stoppedAt), map[string]any{
							"duration": duration.Seconds(),
						})
					} else {
						errorMsg, found, err := unstructured.NestedString(backup.Object, "status", "error")
						if err != nil {
							slog.ErrorContext(ctx, "failed to get backup error", "backup", backupName, "error", err)
							continue
						}

						if !found {
							errorMsg = "unknown error"
						}

						slog.WarnContext(ctx, "backup failed", "scheduledBackup", scheduledBackupName, "backup", backupName, "error", errorMsg)
						outputs.SetError(ctx, fmt.Errorf("backup failed: %s", errorMsg))
					}

					delete(trackedBackups, backupName)
				}

			case watch.Deleted:
				slog.InfoContext(ctx, "backup deleted", "scheduledBackup", scheduledBackupName, "backup", backupName)
				delete(trackedBackups, backupName)
			}
		}
	}
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
