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

export BC_ENV=${BC_ROOT_DIR}/scripts/env

if [ -z "${BC_OUTPUTS_DIR}" ]; then
  export BC_OUTPUTS_DIR=${BC_SCRIPTS_DIR}/outputs
fi
