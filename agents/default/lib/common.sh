#!/bin/bash

function log() {
  echo "[$(date '+%Y-%m-%d %H:%M:%S')] ${*}"
}

check_env_vars() {
  local vars=("$@")
  for var in "${vars[@]}"; do
    if [ -z "${!var}" ]; then
      echo "ERROR: ${var} is not set!"
      exit 1
    fi
  done
}

if [ -z "${BC_ENV}" ]; then
  export BC_ENV=${BC_ROOT_DIR}/scripts/env
fi

if [ -z "${BC_OUTPUTS_DIR}" ]; then
  export BC_OUTPUTS_DIR=${BC_SCRIPTS_DIR}/outputs
fi

if [ -z "${BC_STATE_DIR}" ]; then
  export BC_STATE_DIR=${TMPDIR:-/tmp}/backup-controller-state
fi

# Return a stable, per-repository key derived from RESTIC_REPOSITORY so that
# lock files and maintenance markers of distinct backups never collide.
repo_key() {
  echo -n "${RESTIC_REPOSITORY}" | md5sum | cut -d' ' -f1
}

# maintenance_due <task> <interval_seconds>
# Succeed (task is due) when the interval is empty or <= 0 (which preserves the
# legacy "run on every backup" behavior), or when <task> last ran for the
# current repository more than <interval> seconds ago, or never.
maintenance_due() {
  local task="$1" interval="${2:-0}"
  if [ "${interval}" -le 0 ] 2>/dev/null; then
    return 0
  fi
  local marker="${BC_STATE_DIR}/$(repo_key).${task}"
  if [ ! -f "${marker}" ]; then
    return 0
  fi
  local last now
  last=$(cat "${marker}" 2>/dev/null || echo 0)
  now=$(date +%s)
  [ $((now - last)) -ge "${interval}" ]
}

# maintenance_mark <task> : record that <task> just completed for the repository.
maintenance_mark() {
  local task="$1"
  mkdir -p "${BC_STATE_DIR}"
  echo "$(date +%s)" >"${BC_STATE_DIR}/$(repo_key).${task}"
}
