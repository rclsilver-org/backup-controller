#!/bin/bash

REQUIRED_VARS=("BC_OUTPUT_ICINGA_API_URL" "BC_OUTPUT_ICINGA_USER" "BC_OUTPUT_ICINGA_PASS" "BC_OUTPUT_ICINGA_SERVICE" "BC_OUTPUT_ICINGA_HOST")

function output_init() {
  log "Initializing the Icinga output module."
  check_env_vars "${REQUIRED_VARS[@]}"
  log "Icinga output module initialized."
}

function output_set_success() {
  cat <<EOF | curl -k -s -S -i -u ${BC_OUTPUT_ICINGA_USER}:${BC_OUTPUT_ICINGA_PASS} \
    -H 'Accept: application/json' \
    -X POST "${BC_OUTPUT_ICINGA_API_URL}/v1/actions/process-check-result" \
    -d @-
{
  "type": "Service",
  "filter": "host.name==\"${BC_OUTPUT_ICINGA_HOST}\" && service.name==\"${BC_OUTPUT_ICINGA_SERVICE}\"",
  "exit_status": 0,
  "plugin_output": "${*}",
  "performance_data": [
    "duration=${TOTAL_DURATION}"
  ],
  "pretty": true
}
EOF
}

function output_set_unknown() {
  cat <<EOF | curl -k -s -S -i -u ${BC_OUTPUT_ICINGA_USER}:${BC_OUTPUT_ICINGA_PASS} \
    -H 'Accept: application/json' \
    -X POST "${BC_OUTPUT_ICINGA_API_URL}/v1/actions/process-check-result" \
    -d @-
{
  "type": "Service",
  "filter": "host.name==\"${BC_OUTPUT_ICINGA_HOST}\" && service.name==\"${BC_OUTPUT_ICINGA_SERVICE}\"",
  "exit_status": 3,
  "plugin_output": "${*}",
  "pretty": true
}
EOF
}

function output_set_warning() {
  cat <<EOF | curl -k -s -S -i -u ${BC_OUTPUT_ICINGA_USER}:${BC_OUTPUT_ICINGA_PASS} \
    -H 'Accept: application/json' \
    -X POST "${BC_OUTPUT_ICINGA_API_URL}/v1/actions/process-check-result" \
    -d @-
{
  "type": "Service",
  "filter": "host.name==\"${BC_OUTPUT_ICINGA_HOST}\" && service.name==\"${BC_OUTPUT_ICINGA_SERVICE}\"",
  "exit_status": 1,
  "plugin_output": "${*}",
  "pretty": true
}
EOF
}

function output_set_error() {
  cat <<EOF | curl -k -s -S -i -u ${BC_OUTPUT_ICINGA_USER}:${BC_OUTPUT_ICINGA_PASS} \
    -H 'Accept: application/json' \
    -X POST "${BC_OUTPUT_ICINGA_API_URL}/v1/actions/process-check-result" \
    -d @-
{
  "type": "Service",
  "filter": "host.name==\"${BC_OUTPUT_ICINGA_HOST}\" && service.name==\"${BC_OUTPUT_ICINGA_SERVICE}\"",
  "exit_status": 2,
  "plugin_output": "${*}",
  "pretty": true
}
EOF
}
