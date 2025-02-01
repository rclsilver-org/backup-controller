#!/bin/bash

set -e

if [ -z "${BC_SCRIPTS_DIR}" ]; then
  export BC_SCRIPTS_DIR="${BC_ROOT_DIR}/scripts"
fi

source ${BC_SCRIPTS_DIR}/lib/common.sh

REQUIRED_VARS=("BC_SCHEDULE" "RESTIC_REPOSITORY" "RESTIC_PASSWORD")

check_env_vars "${REQUIRED_VARS[@]}"

if [ -z "${BC_CMD}" ] && [ -z "${BC_BACKUP_DIR}" ]; then
  echo "ERROR: bot BC_CMD and BC_BACKUP_DIR are not set!"
  exit 1
fi

if [ ! -z "${TZDATA}" ]; then
  if [ ! -f /usr/share/zoneinfo/${TZDATA} ]; then
    echo "WARNING: invalid zone name: '${TZDATA}'" >&2
  else
    ln -sf /usr/share/zoneinfo/${TZDATA} /etc/localtime
  fi
fi

# Generate the environment file
DEFAULT_VARS=("^BC_" "^RESTIC_" "^AWS_")
EXTRA_VARS_ARRAY=()
if [ -n "${EXTRA_VARS}" ]; then
  read -a EXTRA_VARS_ARRAY <<<"${EXTRA_VARS}"
fi
ALL_VARS=("${DEFAULT_VARS[@]}" "${EXTRA_VARS_ARRAY[@]}")
(
  env | while IFS='=' read -r VAR_NAME VAR_VALUE; do
    for pattern in "${ALL_VARS[@]}"; do
      if [[ "${VAR_NAME}" =~ ${pattern} ]]; then
        echo "export ${VAR_NAME}='${VAR_VALUE}'"
        break
      fi
    done
  done
) >${BC_ENV}

# Generate the crontab file
CRON_FILE=/etc/crontabs/root
(
  echo "SHELL=/bin/bash"
  echo "PATH=${PATH}"
  echo "${BC_SCHEDULE} BC_ROOT_DIR=${BC_ROOT_DIR} ${BC_ROOT_DIR}/scripts/run-backup.sh >> /proc/1/fd/1 2>&1"
) >${CRON_FILE}

# Start crond in the foreground
log "Starting crond..."
exec crond -f -s -m off
