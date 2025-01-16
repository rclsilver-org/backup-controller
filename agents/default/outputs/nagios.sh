#!/bin/bash

REQUIRED_VARS=("BC_OUTPUT_NAGIOS_NSCA_HOST" "BC_OUTPUT_NAGIOS_HOST" "BC_OUTPUT_NAGIOS_SERVICE")

function output_init() {
  log "Initializing the nagios output module."
  check_env_vars "${REQUIRED_VARS[@]}"
  log "Nagios output module initialized."
}

function output_set_success() {
  echo -e "${BC_OUTPUT_NAGIOS_HOST}\t${BC_OUTPUT_NAGIOS_SERVICE}\t0\tOK - ${*}" | send_nsca -H "${BC_OUTPUT_NAGIOS_NSCA_HOST}" -c /etc/send_nsca.cfg
}

function output_set_unknown() {
  echo -e "${BC_OUTPUT_NAGIOS_HOST}\t${BC_OUTPUT_NAGIOS_SERVICE}\t3\tUNKNOWN - ${*}" | send_nsca -H "${BC_OUTPUT_NAGIOS_NSCA_HOST}" -c /etc/send_nsca.cfg
}

function output_set_warning() {
  echo -e "${BC_OUTPUT_NAGIOS_HOST}\t${BC_OUTPUT_NAGIOS_SERVICE}\t1\tWARNING - ${*}" | send_nsca -H "${BC_OUTPUT_NAGIOS_NSCA_HOST}" -c /etc/send_nsca.cfg
}

function output_set_error() {
  echo -e "${BC_OUTPUT_NAGIOS_HOST}\t${BC_OUTPUT_NAGIOS_SERVICE}\t2\tKO - ${*}" | send_nsca -H "${BC_OUTPUT_NAGIOS_NSCA_HOST}" -c /etc/send_nsca.cfg
}
