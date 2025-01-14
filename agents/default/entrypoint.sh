#!/bin/bash

set -e

if [ -z "${BACKUP_CMD}" ]; then
  echo "Error: BACKUP_CMD is not set!"
  exit 1
fi

if [ -z "${BACKUP_SCHEDULE}" ]; then
  echo "Error: BACKUP_SCHEDULE is not set!"
  exit 1
fi

if [ ! -z "${TZDATA}" ]; then
  if [ ! -f /usr/share/zoneinfo/${TZDATA} ]; then
    echo "warning: invalid zone name: '${TZDATA}'" >&2
  else
    ln -sf /usr/share/zoneinfo/${TZDATA} /etc/localtime
  fi
fi

# Generate the environment file
DEFAULT_VARS=("^BACKUP_" "^RESTIC_" "^AWS_")
EXTRA_VARS_ARRAY=()
if [ -n "${EXTRA_VARS}" ]; then
  read -a EXTRA_VARS_ARRAY <<< "${EXTRA_VARS}"
fi
ALL_VARS=("${DEFAULT_VARS[@]}" "${EXTRA_VARS_ARRAY[@]}")
ENV_FILE=/root/env
(
  env | while IFS='=' read -r VAR_NAME VAR_VALUE; do
    for pattern in "${ALL_VARS[@]}"; do
      if [[ "${VAR_NAME}" =~ ${pattern} ]]; then
        echo "export ${VAR_NAME}='${VAR_VALUE}'"
        break
      fi
    done
  done
) > ${ENV_FILE}

# Generate the crontab file
CRON_FILE=/etc/crontabs/root
(
  echo "SHELL=/bin/bash"
  echo "PATH=${PATH}"
  echo "${BACKUP_SCHEDULE} ENV_FILE=${ENV_FILE} /opt/backup-controller/scripts/command-wrapper.sh >> /proc/1/fd/1 2>&1"
) > ${CRON_FILE}

# Start crond in the foreground
echo "[$(date '+%Y-%m-%d %H:%M:%S')] Starting crond..."
exec crond -f -s -m off
