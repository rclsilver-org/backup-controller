#!/bin/bash

if [ -z "${BC_SCRIPTS_DIR}" ]; then
  export BC_SCRIPTS_DIR="${BC_ROOT_DIR}/scripts"
fi

source ${BC_SCRIPTS_DIR}/lib/common.sh

if [ -f "${BC_ENV}" ]; then
  source ${BC_ENV}
fi

log "Starting a PostgreSQL backup process."

# Retrieve the PostgreSQL server version to determine which backup functions to use
PG_VERSION=$(psql -t -c "SHOW server_version;" | cut -d '.' -f1 | tr -d ' ')

log "Detected PostgreSQL version: ${PG_VERSION}"

if [ "${PG_VERSION}" -ge 15 ]; then
  START_BACKUP_FUNCTION="pg_backup_start"
  STOP_BACKUP_FUNCTION="pg_backup_stop"
else
  START_BACKUP_FUNCTION="pg_start_backup"
  STOP_BACKUP_FUNCTION="pg_stop_backup"
fi

# Attempt to start PostgreSQL backup
if ! psql -c "SELECT ${START_BACKUP_FUNCTION}('$(date +%Y-%m-%d)')"; then
  log "ERROR: Failed to initiate PostgreSQL backup. Check the database logs for more details."
  exit 1
fi
log "PostgreSQL backup mode enabled."

# Perform the Restic backup
log "Starting Restic backup of the directory: ${PGDATA}."
if ! restic backup "${PGDATA}"; then
  log "ERROR: Restic backup failed. Ensure that Restic is correctly configured and accessible."
  psql -c "SELECT ${STOP_BACKUP_FUNCTION}()" >/dev/null 2>&1 || log "WARNING: Failed to exit PostgreSQL backup mode after Restic error."
  exit 1
fi
log "Restic backup completed successfully."

# Attempt to stop PostgreSQL backup
if ! psql -c "SELECT ${STOP_BACKUP_FUNCTION}()"; then
  log "ERROR: Failed to disable PostgreSQL backup mode. The database may remain in backup mode."
  exit 1
fi
log "PostgreSQL backup mode disabled."

# Final success log
log "PostgreSQL backup process completed successfully."
