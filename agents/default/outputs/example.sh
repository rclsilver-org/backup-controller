#!/bin/bash

function output_init() {
  echo "Initialization of the output module"
}

function output_set_success() {
  echo "Output is set to 'success' with message: ${*}"
}

function output_set_unknown() {
  echo "Output is set to 'unknown' with message: ${*}"
}

function output_set_warning() {
  echo "Output is set to 'warning' with message: ${*}"
}

function output_set_error() {
  echo "Output is set to 'error' with message: ${*}"
}
