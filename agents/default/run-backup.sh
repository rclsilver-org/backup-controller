#!/bin/bash

set -e

source ${BC_ROOT_DIR}/scripts/lib/common.sh
source ${BC_ENV}

log "Starting the backup process."

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

# Execute the backup command
log "Executing the backup command: ${BC_CMD}"
if ${BC_CMD}; then
  log "Backup command executed successfully."
else
  log "ERROR: Backup command failed. Please check the logs and configuration."
  exit 1
fi

# Purge older snapshots if needed
if [ ! -z "${BACKUP_RETENTION_DAYS}" ] && [ "${BACKUP_RETENTION_DAYS}" -gt 0 ]; then
  log "Starting the cleanup of older snapshots. Retention policy: keep snapshots from the last ${BACKUP_RETENTION_DAYS} days."

  if restic forget -d ${BACKUP_RETENTION_DAYS} -c --prune; then
    log "Older snapshots have been successfully pruned based on the retention policy."
  else
    log "ERROR: Failed to prune older snapshots. Please check the Restic logs for details."
    exit 1
  fi
else
  log "No snapshot retention policy defined or retention count is set to 0. Skipping snapshot pruning."
fi

log "Performing a repository integrity check."
if restic check; then
  log "Repository integrity check completed successfully. No errors found."
else
  log "ERROR: Repository integrity check failed. Please investigate the issue."
  exit 1
fi

log "Backup process completed successfully."
