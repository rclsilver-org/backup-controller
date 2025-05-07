ARG RESTIC_VERSION=0.17.3
ARG ALPINE_VERSION=3.18

FROM alpine:${ALPINE_VERSION} as base

RUN apk add --no-cache git gcc libc-dev make && \
    cd /root && \
    git clone https://github.com/NagiosEnterprises/nsca.git && \
    cd nsca && \
    ./configure && \
    make all

FROM restic/restic:${RESTIC_VERSION} AS restic
FROM base

RUN apk add --no-cache bash curl cronie tzdata jq

COPY --from=restic /usr/bin/restic /usr/bin/restic
COPY --from=base /root/nsca/src/send_nsca /usr/local/bin/send_nsca
COPY --from=base /root/nsca/sample-config/send_nsca.cfg /etc/send_nsca.cfg
COPY . /opt/backup-controller/scripts

ENV BC_ROOT_DIR='/opt/backup-controller'
ENV BC_SCHEDULE="0 0 * * *"
ENV BC_RETENTION_DAYS="7"

ENTRYPOINT ["/opt/backup-controller/scripts/entrypoint.sh"]
