ARG RESTIC_VERSION=0.17.3
ARG ALPINE_VERSION=3.18

FROM restic/restic:${RESTIC_VERSION} AS restic
FROM alpine:${ALPINE_VERSION}

RUN apk add --no-cache bash curl cronie tzdata

COPY --from=restic /usr/bin/restic /usr/bin/restic
COPY . /opt/backup-controller/scripts

ENV BC_ROOT_DIR='/opt/backup-controller'
ENV BC_SCHEDULE="0 0 * * *"
ENV BC_RETENTION_DAYS="7"

ENTRYPOINT ["/opt/backup-controller/scripts/entrypoint.sh"]
