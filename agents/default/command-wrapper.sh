#!/bin/bash

set -e

source /opt/backup-controller/scripts/common.sh
source "${ENV_FILE}"

log "Checking the Restic repository."

# Verify or initialize the Restic repository
if restic snapshots >/dev/null 2>&1; then
    log "Restic repository is accessible and valid."
else
    log "Restic repository not found. Initializing a new repository."
    if restic init; then
        log "Restic repository initialized successfully."
    else
        log "ERROR: Failed to initialize Restic repository. Please check your configuration."
        exit 1
    fi
fi

log "Starting the backup process."

# Execute the backup command
log "Executing the backup command: ${BACKUP_CMD}"
if ${BACKUP_CMD}; then
    log "Backup command executed successfully."
else
    log "ERROR: Backup command failed. Please check the logs and configuration."
    exit 1
fi

log "Backup process completed successfully."
