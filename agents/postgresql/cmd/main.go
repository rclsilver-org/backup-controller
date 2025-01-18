package main

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strconv"
	"time"

	_ "github.com/lib/pq"

	"github.com/rclsilver-org/backup-controller/agents/common"
)

const (
	PGHOST     = "PGHOST"
	PGUSER     = "PGUSER"
	PGPASSWORD = "PGPASSWORD"
	PGDATA     = "PGDATA"
)

func main() {
	ctx := context.Background()

	logLevel := slog.LevelInfo
	if common.IsDebug() {
		logLevel = slog.LevelDebug
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: logLevel,
	}))

	if err := common.RequiredEnvVar(PGHOST, PGUSER, PGPASSWORD, PGDATA); err != nil {
		logger.ErrorContext(ctx, "unable to verify environment variables", "error", err)
		os.Exit(1)
	}

	pgdata := os.Getenv(PGDATA)
	if err := common.IsDirectory(pgdata); err != nil {
		logger.ErrorContext(ctx, "postgresql data directory is not readable", "error", err)
		os.Exit(1)
	}

	pghost := os.Getenv(PGHOST)
	pguser := os.Getenv(PGUSER)
	pgpassword := os.Getenv(PGPASSWORD)

	logger.Debug("starting the PostgreSQL backup process", "server", pghost, "user", pguser)

	// Connect to PostgreSQL
	db, err := sql.Open("postgres", fmt.Sprintf("host=%s port=5432 user=%s password=%s sslmode=disable", pghost, pguser, pgpassword))
	if err != nil {
		logger.Error("failed to connect to the PostgreSQL server", "error", err)
	}
	defer db.Close()
	logger.Debug("connected to the PostgreSQL server", "server", pghost, "user", pguser)

	// Determine the PostgreSQL version
	version, err := pgGetVersion(db)
	if err != nil {
		logger.Error("unable to fetch the server version", "error", err)
		os.Exit(1)
	}
	logger.Info("fetched the server version", "version", version)

	// Enable the backup mode
	if err := pgStartBackup(ctx, db, version); err != nil {
		logger.Error("unable to enable the backup mode", "error", err)
		os.Exit(1)
	}
	logger.Info("backup mode is now enabled")

	// Execute restic
	if err := executeRestic(ctx, pgdata); err != nil {
		logger.Error("restic backup failed", "error", err)
		if err := pgStopBackup(ctx, db, version); err != nil {
			logger.Error("unable to disable the backup mode", "error", err)
		} else {
			logger.Info("backup mode is now disabled")
		}
		os.Exit(1)
	}

	// Disable the backup mode
	if err := pgStopBackup(ctx, db, version); err != nil {
		logger.Error("unable to disable the backup mode", "error", err)
		os.Exit(1)
	}
	logger.Info("backup mode is now disabled")
}

// pgGetVersion fetch and return the PostgreSQL server version
func pgGetVersion(db *sql.DB) (float64, error) {
	var version string
	err := db.QueryRow("SHOW server_version").Scan(&version)
	if err != nil {
		return 0, err
	}

	result, err := strconv.ParseFloat(version, 64)
	if err != nil {
		return 0, fmt.Errorf("unable to parse the version string: %w", err)
	}

	return result, nil
}

// pgStartBackup enables the backup in PostgreSQL
func pgStartBackup(ctx context.Context, db *sql.DB, pgVersion float64) error {
	startBackupFunc := "pg_backup_start"
	if pgVersion < 15 {
		startBackupFunc = "pg_start_backup"
	}

	query := fmt.Sprintf("SELECT %s($1)", startBackupFunc)
	if _, err := db.ExecContext(ctx, query, time.Now().Format("2006-01-02 15:04:05")); err != nil {
		return err
	}

	return nil
}

// pgStopBackup disables the backup mode in PostgreSQL
func pgStopBackup(ctx context.Context, db *sql.DB, pgVersion float64) error {
	stopBackupFunc := "pg_backup_stop"
	if pgVersion < 15 {
		stopBackupFunc = "pg_stop_backup"
	}

	query := fmt.Sprintf("SELECT %s()", stopBackupFunc)
	if _, err := db.ExecContext(ctx, query); err != nil {
		return err
	}

	return nil
}

// executeRestic launches a restic backup of the given path
func executeRestic(ctx context.Context, path string) error {
	cmd := exec.CommandContext(ctx, "restic", "backup", path)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	return cmd.Run()
}
