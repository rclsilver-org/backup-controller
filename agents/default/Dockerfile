ARG RESTIC_VERSION=0.17.3
ARG ALPINE_VERSION=3.18

FROM restic/restic:${RESTIC_VERSION} AS restic
FROM alpine:${ALPINE_VERSION}

RUN apk add --no-cache bash curl cronie tzdata

COPY --from=restic /usr/bin/restic /usr/bin/restic

COPY entrypoint.sh /opt/backup-controller/scripts/entrypoint.sh
COPY common.sh /opt/backup-controller/scripts/common.sh
COPY command-wrapper.sh /opt/backup-controller/scripts/command-wrapper.sh

ENV BACKUP_SCHEDULE="0 0 * * *"

ENTRYPOINT ["/opt/backup-controller/scripts/entrypoint.sh"]
