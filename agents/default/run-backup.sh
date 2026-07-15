#!/bin/bash

set -e

if [ -z "${BC_SCRIPTS_DIR}" ]; then
  export BC_SCRIPTS_DIR="${BC_ROOT_DIR}/scripts"
fi

source ${BC_SCRIPTS_DIR}/lib/common.sh

if [ -f "${BC_ENV}" ]; then
  source ${BC_ENV}
fi

# Prevent concurrent runs against the same repository. A slow prune or check
# must never let the next scheduled run pile up on top of it: overlapping runs
# stack restic locks and I/O and, over time, can wedge the whole repository.
# The lock is keyed on the repository so distinct backups never block each other.
if [ -z "${BC_LOCK_FILE}" ]; then
  BC_LOCK_FILE="${TMPDIR:-/tmp}/backup-controller-$(repo_key).lock"
fi
exec 9>"${BC_LOCK_FILE}"
if ! flock -n 9; then
  log "Another backup run for this repository is already in progress; skipping."
  exit 0
fi

START_TIME=$(date +%s)

log "Starting the backup process."

# Initialize the output module
if [ ! -z "${BC_OUTPUT_MODULE}" ]; then
  if [ ! -f "${BC_OUTPUTS_DIR}/${BC_OUTPUT_MODULE}.sh" ]; then
    log "Output module '${BC_OUTPUT_MODULE}' not found!"
    exit 1
  fi
  source "${BC_OUTPUTS_DIR}/${BC_OUTPUT_MODULE}.sh"

  if ! output_init; then
    log "ERROR: Failed to initialize the '${BC_OUTPUT_MODULE}' output module. Please check your configuration."
    exit 1
  fi
  log "Output module '${BC_OUTPUT_MODULE}' initialized successfully."
else
  source "${BC_OUTPUTS_DIR}/void.sh"
fi

# Verify or initialize the Restic repository.
# Probe the state with a lock-free `restic cat config` and branch on restic's
# exit codes (restic >= 0.17). Using --no-lock makes this immune to any lock
# left by a concurrent or interrupted run, so a locked repository is never
# mistaken for a missing one:
#   0  -> repository is present and readable
#   10 -> repository does not exist -> initialize it
#   *  -> any other error (locked, wrong password, unreachable) -> fail loudly
#         and DO NOT init, to avoid clobbering an existing repository.
rc=0
restic cat config --no-lock >/dev/null 2>&1 || rc=$?
if [ "${rc}" -eq 0 ]; then
  log "Restic repository is accessible and valid."
elif [ "${rc}" -eq 10 ]; then
  log "Restic repository does not exist. Initializing a new repository."
  if restic init; then
    log "Restic repository initialized successfully."
  else
    log "ERROR: Failed to initialize Restic repository. Please check your configuration."
    output_set_error "restic repository initialization failed at $(date '+%Y-%m-%d %H:%M:%S')"
    exit 1
  fi
else
  log "ERROR: Restic repository unreachable (exit code ${rc}); not initializing to avoid clobbering an existing repository."
  output_set_error "restic repository check failed with exit code ${rc} at $(date '+%Y-%m-%d %H:%M:%S')"
  exit 1
fi

# Compute the command if not set
if [ -z "${BC_CMD}" ]; then
  BC_LIST_FILES="/tmp/list-files.txt"
  IFS=':' read -r -a BC_PATHS <<< "$BC_BACKUP_DIR"
  > ${BC_LIST_FILES}
  BC_EXCLUDE_ARGS=""
  for path in "${BC_PATHS[@]}"; do
      echo "$path" >> ${BC_LIST_FILES}
      # Honor a per-path ignore file listing exclude patterns (one per line)
      if [ -f "${path}/.restic-ignore" ]; then
          BC_EXCLUDE_ARGS="${BC_EXCLUDE_ARGS} --exclude-file=${path}/.restic-ignore"
      fi
  done
  BC_CMD="restic backup --files-from=${BC_LIST_FILES}${BC_EXCLUDE_ARGS}"
fi

# Execute the backup command
log "Executing the backup command: ${BC_CMD}"
rc=0
${BC_CMD} || rc=$?
if [ "${rc}" -eq 11 ]; then
  # Repository is locked (restic exit code 11). It may be a stale lock left by
  # an interrupted run, or a backup still in progress. `restic unlock` (without
  # --remove-all) only removes locks restic itself considers stale, so a lock
  # actively refreshed by a running backup is preserved and the retry will
  # correctly fail again. Retry once.
  log "Repository is locked (exit 11). Removing stale locks and retrying once."
  restic unlock || true
  rc=0
  ${BC_CMD} || rc=$?
fi
if [ "${rc}" -eq 0 ]; then
  log "Backup command executed successfully."
else
  log "ERROR: Backup command failed (exit code ${rc}). Please check the logs and configuration."
  output_set_error "backup command execution failed at $(date '+%Y-%m-%d %H:%M:%S')"
  exit 1
fi

# Enforce the retention policy on every run. `restic forget` only rewrites
# snapshot references, which is cheap; the expensive repacking of unused data is
# done separately by `prune` below, on its own (less frequent) cadence.
if [ ! -z "${BC_RETENTION_DAYS}" ] && [ "${BC_RETENTION_DAYS}" -gt 0 ]; then
  log "Applying retention policy: keep snapshots from the last ${BC_RETENTION_DAYS} days."

  if restic forget -d ${BC_RETENTION_DAYS} -c; then
    log "Retention policy applied successfully."
  else
    log "ERROR: Failed to apply the retention policy. Please check the Restic logs for details."
    output_set_error "snapshot forget failed at $(date '+%Y-%m-%d %H:%M:%S')"
    exit 1
  fi
else
  log "No snapshot retention policy defined or retention count is set to 0. Skipping snapshot forget."
fi

# Prune (repack unused data) is I/O heavy on the object store, so it runs at most
# once every BC_PRUNE_INTERVAL seconds instead of on every backup. Leaving
# BC_PRUNE_INTERVAL unset or 0 keeps the previous behavior (prune on every run).
if maintenance_due "prune" "${BC_PRUNE_INTERVAL:-0}"; then
  log "Pruning the repository (repacking unused data)."
  if restic prune; then
    maintenance_mark "prune"
    log "Repository pruned successfully."
  else
    log "ERROR: Failed to prune the repository. Please check the Restic logs for details."
    output_set_error "repository prune failed at $(date '+%Y-%m-%d %H:%M:%S')"
    exit 1
  fi
else
  log "Skipping prune; not due yet (BC_PRUNE_INTERVAL=${BC_PRUNE_INTERVAL}s)."
fi

# The integrity check reads the whole repository, so it also runs at most once
# every BC_CHECK_INTERVAL seconds. Unset or 0 keeps checking on every run.
if maintenance_due "check" "${BC_CHECK_INTERVAL:-0}"; then
  log "Performing a repository integrity check."
  if restic check; then
    maintenance_mark "check"
    log "Repository integrity check completed successfully. No errors found."
  else
    log "ERROR: Repository integrity check failed. Please investigate the issue."
    output_set_error "restic repository integrity check failed at $(date '+%Y-%m-%d %H:%M:%S')"
    exit 1
  fi
else
  log "Skipping integrity check; not due yet (BC_CHECK_INTERVAL=${BC_CHECK_INTERVAL}s)."
fi

#  Send the final status to the output module
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

# Restic latest snapshot
SNAPSHOT_SUMMARY=$(restic snapshots --latest 1 --json | jq -r '.[0].summary')

export BACKUP_FILES_NEW=$(echo ${SNAPSHOT_SUMMARY} | jq -r '.files_new')
export BACKUP_FILES_CHANGED=$(echo ${SNAPSHOT_SUMMARY} | jq -r '.files_changed')
export BACKUP_FILES_UNMODIFIED=$(echo ${SNAPSHOT_SUMMARY} | jq -r '.files_unmodified')
export BACKUP_DIRS_NEW=$(echo ${SNAPSHOT_SUMMARY} | jq -r '.dirs_new')
export BACKUP_DIRS_CHANGED=$(echo ${SNAPSHOT_SUMMARY} | jq -r '.dirs_changed')
export BACKUP_DIRS_UNMODIFIED=$(echo ${SNAPSHOT_SUMMARY} | jq -r '.dirs_unmodified')
export BACKUP_DATA_BLOBS=$(echo ${SNAPSHOT_SUMMARY} | jq -r '.data_blobs')
export BACKUP_TREE_BLOBS=$(echo ${SNAPSHOT_SUMMARY} | jq -r '.tree_blobs')
export BACKUP_DATA_ADDED=$(echo ${SNAPSHOT_SUMMARY} | jq -r '.data_added')
export BACKUP_DATA_ADDED_PACKED=$(echo ${SNAPSHOT_SUMMARY} | jq -r '.data_added_packed')
export BACKUP_TOTAL_FILES=$(echo ${SNAPSHOT_SUMMARY} | jq -r '.total_files_processed')
export BACKUP_TOTAL_BYTES=$(echo ${SNAPSHOT_SUMMARY} | jq -r '.total_bytes_processed')
export BACKUP_DURATION=${TOTAL_DURATION}

output_set_success "backup process completed successfully in ${HUMAN_DURATION} at $(date '+%Y-%m-%d %H:%M:%S')"

log "Backup process completed successfully in ${HUMAN_DURATION}."
