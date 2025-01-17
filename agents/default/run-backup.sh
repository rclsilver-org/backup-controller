#!/bin/bash

set -e

if [ -z "${BC_SCRIPTS_DIR}" ]; then
  export BC_SCRIPTS_DIR="${BC_ROOT_DIR}/scripts"
fi

source ${BC_SCRIPTS_DIR}/lib/common.sh

if [ -f "${BC_ENV}" ]; then
  source ${BC_ENV}
fi

START_TIME=$(date +%s)

log "Starting the backup process."

# Initialize the output module
if [ ! -z "${BC_OUTPUT_MODULE}" ]; then
  if [ ! -f "${BC_OUTPUTS_DIR}/${BC_OUTPUT_MODULE}.sh" ]; then
    log "Output module '${BC_OUTPUT_MODULE}' not found!"
    output_set_error "unknown '${BC_OUTPUT_MODULE}' output module"
    exit 1
  fi
  source "${BC_OUTPUTS_DIR}/${BC_OUTPUT_MODULE}.sh"

  if ! output_init; then
    log "ERROR: Failed to initialize the '${BC_OUTPUT_MODULE}' output module. Please check your configuration."
    output_set_error "failed to initialize the '${BC_OUTPUT_MODULE}' output module"
    exit 1
  fi
  log "Output module '${BC_OUTPUT_MODULE}' initialized successfully."
else
  source "${BC_OUTPUTS_DIR}/void.sh"
fi

# Verify or initialize the Restic repository
if restic snapshots >/dev/null 2>&1; then
  log "Restic repository is accessible and valid."
else
  log "Restic repository not found. Initializing a new repository."
  if restic init; then
    log "Restic repository initialized successfully."
  else
    log "ERROR: Failed to initialize Restic repository. Please check your configuration."
    output_set_error "failed to initialize the Restic repository"
    exit 1
  fi
fi

# Execute the backup command
log "Executing the backup command: ${BC_CMD}"
if ${BC_CMD}; then
  log "Backup command executed successfully."
else
  log "ERROR: Backup command failed. Please check the logs and configuration."
  output_set_error "backup command failed"
  exit 1
fi

# Purge older snapshots if needed
if [ ! -z "${BACKUP_RETENTION_DAYS}" ] && [ "${BACKUP_RETENTION_DAYS}" -gt 0 ]; then
  log "Starting the cleanup of older snapshots. Retention policy: keep snapshots from the last ${BACKUP_RETENTION_DAYS} days."

  if restic forget -d ${BACKUP_RETENTION_DAYS} -c --prune; then
    log "Older snapshots have been successfully pruned based on the retention policy."
  else
    log "ERROR: Failed to prune older snapshots. Please check the Restic logs for details."
    output_set_error "failed to prune older snapshots"
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
  output_set_error "restic repository integrity check failed"
  exit 1
fi

END_TIME=$(date +%s)
TOTAL_DURATION=$((END_TIME - START_TIME))
HOURS=$((TOTAL_DURATION / 3600))
MINUTES=$(((TOTAL_DURATION % 3600) / 60))
SECONDS=$((TOTAL_DURATION % 60))

HUMAN_DURATION="${SECONDS}s"
if [ ${MINUTES} -gt 0 ]; then
  HUMAN_DURATION="${MINUTES}m ${HUMAN_DURATION}"
fi

if [ ${HOURS} -gt 0 ]; then
  HUMAN_DURATION="${HOURS}h ${HUMAN_DURATION}"
fi

log "Backup process completed successfully."
output_set_success "done in ${HUMAN_DURATION}"
