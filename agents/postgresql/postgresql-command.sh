#!/bin/bash

source /opt/backup-controller/scripts/common.sh

log "Starting a PostgreSQL backup process."

# Attempt to start PostgreSQL backup
if ! psql -c "SELECT pg_start_backup('$(date +%Y-%m-%d)')"; then
    log "ERROR: Failed to initiate PostgreSQL backup. Check the database logs for more details."
    exit 1
fi
log "PostgreSQL backup mode enabled."

# Perform the Restic backup
log "Starting Restic backup of the directory: ${PGDATA}."
if ! restic backup "${PGDATA}"; then
    log "ERROR: Restic backup failed. Ensure that Restic is correctly configured and accessible."
    psql -c "SELECT pg_stop_backup()" > /dev/null 2>&1 || log "WARNING: Failed to exit PostgreSQL backup mode after Restic error."
    exit 1
fi
log "Restic backup completed successfully."

# Attempt to stop PostgreSQL backup
if ! psql -c "SELECT pg_stop_backup()"; then
    log "ERROR: Failed to disable PostgreSQL backup mode. The database may remain in backup mode."
    exit 1
fi
log "PostgreSQL backup mode disabled."

# Final success log
log "PostgreSQL backup process completed successfully."
exit 0
