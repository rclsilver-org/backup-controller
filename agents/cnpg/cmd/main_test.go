package main

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

const testOwner = "cnpg-immich"

// makeBackup builds an unstructured Backup owned by testOwner for tests.
func makeBackup(name, phase, startedAt, stoppedAt, errMsg string, creation time.Time) unstructured.Unstructured {
	u := unstructured.Unstructured{Object: map[string]any{}}
	u.SetName(name)
	u.SetKind("Backup")
	u.SetCreationTimestamp(metav1.NewTime(creation))
	u.SetOwnerReferences([]metav1.OwnerReference{{Kind: "ScheduledBackup", Name: testOwner}})

	status := map[string]any{}
	if phase != "" {
		status["phase"] = phase
	}
	if startedAt != "" {
		status["startedAt"] = startedAt
	}
	if stoppedAt != "" {
		status["stoppedAt"] = stoppedAt
	}
	if errMsg != "" {
		status["error"] = errMsg
	}
	if len(status) > 0 {
		u.Object["status"] = status
	}
	return u
}

func listOf(items ...unstructured.Unstructured) *unstructured.UnstructuredList {
	return &unstructured.UnstructuredList{Items: items}
}

func TestEvaluateBackupHealth(t *testing.T) {
	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	interval := 24 * time.Hour
	rfc := func(d time.Duration) string { return now.Add(-d).Format(time.RFC3339) }

	tests := []struct {
		name      string
		list      *unstructured.UnstructuredList
		wantLevel int
	}{
		{
			name:      "no backups yet",
			list:      listOf(),
			wantLevel: healthUnknown,
		},
		{
			name: "fresh successful backup",
			list: listOf(
				makeBackup("b1", backupPhaseCompleted, rfc(2*time.Hour+time.Minute), rfc(2*time.Hour), "", now.Add(-2*time.Hour)),
			),
			wantLevel: healthOK,
		},
		{
			name: "successful backup slightly overdue",
			list: listOf(
				makeBackup("b1", backupPhaseCompleted, rfc(30*time.Hour), rfc(30*time.Hour), "", now.Add(-30*time.Hour)),
			),
			wantLevel: healthWarning,
		},
		{
			name: "successful backup very overdue",
			list: listOf(
				makeBackup("b1", backupPhaseCompleted, rfc(12*24*time.Hour), rfc(12*24*time.Hour), "", now.Add(-12*24*time.Hour)),
			),
			wantLevel: healthCritical,
		},
		{
			name: "backup stuck in started blocks queue",
			list: listOf(
				makeBackup("old", backupPhaseCompleted, rfc(13*24*time.Hour), rfc(13*24*time.Hour), "", now.Add(-13*24*time.Hour)),
				makeBackup("stuck", "started", rfc(11*24*time.Hour), "", "", now.Add(-11*24*time.Hour)),
				// A pile of queued backups with empty status behind the stuck one.
				makeBackup("queued", "", "", "", "", now.Add(-3*24*time.Hour)),
			),
			wantLevel: healthCritical,
		},
		{
			name: "queued backup with empty status past deadline",
			list: listOf(
				makeBackup("old", backupPhaseCompleted, rfc(5*24*time.Hour), rfc(5*24*time.Hour), "", now.Add(-5*24*time.Hour)),
				makeBackup("queued", "", "", "", "", now.Add(-4*24*time.Hour)),
			),
			wantLevel: healthCritical,
		},
		{
			name: "most recent terminal backup failed",
			list: listOf(
				makeBackup("ok", backupPhaseCompleted, rfc(26*time.Hour), rfc(26*time.Hour), "", now.Add(-26*time.Hour)),
				makeBackup("ko", backupPhaseFailed, rfc(2*time.Hour), rfc(1*time.Hour), "S3 connection refused", now.Add(-2*time.Hour)),
			),
			wantLevel: healthCritical,
		},
		{
			name: "first backup in progress, none finished",
			list: listOf(
				makeBackup("running", "started", rfc(5*time.Minute), "", "", now.Add(-5*time.Minute)),
			),
			wantLevel: healthUnknown,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := evaluateBackupHealth(tc.list, testOwner, interval, now)
			if got.level != tc.wantLevel {
				t.Fatalf("level = %d, want %d (msg: %q)", got.level, tc.wantLevel, got.msg)
			}
		})
	}
}

func TestParseScheduleInterval(t *testing.T) {
	tests := []struct {
		schedule string
		want     time.Duration
	}{
		{"0 0 0 * * *", 24 * time.Hour},      // daily
		{"0 0 */6 * * *", 6 * time.Hour},     // every 6 hours
		{"0 0 * * * *", 1 * time.Hour},       // hourly
		{"0 */30 * * * *", 30 * time.Minute}, // every 30 minutes
	}
	for _, tc := range tests {
		t.Run(tc.schedule, func(t *testing.T) {
			got, err := parseScheduleInterval(tc.schedule)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("interval = %s, want %s", got, tc.want)
			}
		})
	}

	if _, err := parseScheduleInterval("not a cron"); err == nil {
		t.Fatal("expected error for invalid schedule")
	}
}

func clusterWithPrimary(primary string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"status": map[string]any{"currentPrimary": primary},
	}}
}

func drainReReport() {
	select {
	case <-reReport:
	default:
	}
}

func nudged() bool {
	select {
	case <-reReport:
		return true
	default:
		return false
	}
}

// TestSetPrimary covers the role gating: isPrimary tracks currentPrimary==myPod, and
// reReport is nudged only when the role actually flips (so the new primary pushes
// immediately on failover while steady state produces no churn).
func TestSetPrimary(t *testing.T) {
	ctx := context.Background()
	const me = "cnpg-mealie-1"

	isPrimary.Store(false)
	drainReReport()

	setPrimary(ctx, clusterWithPrimary(me), me)
	if !isPrimary.Load() {
		t.Fatal("expected isPrimary=true after promotion")
	}
	if !nudged() {
		t.Fatal("expected a reReport nudge on promotion")
	}

	setPrimary(ctx, clusterWithPrimary(me), me)
	if nudged() {
		t.Fatal("did not expect a nudge when the role is unchanged")
	}

	setPrimary(ctx, clusterWithPrimary("cnpg-mealie-2"), me)
	if isPrimary.Load() {
		t.Fatal("expected isPrimary=false after demotion")
	}
	if !nudged() {
		t.Fatal("expected a reReport nudge on demotion")
	}

	isPrimary.Store(true)
	drainReReport()
	setPrimary(ctx, clusterWithPrimary(""), me)
	if isPrimary.Load() {
		t.Fatal("expected isPrimary=false when currentPrimary is empty")
	}
}
