ARG VERSION=latest

FROM ghcr.io/rclsilver-org/backup-controller-agent-default:${VERSION}

RUN apk add --update --no-cache postgresql-client

COPY postgresql-command.sh /opt/backup-controller/scripts/postgresql-command.sh

ENV BACKUP_CMD='/opt/backup-controller/scripts/postgresql-command.sh'
ENV EXTRA_VARS='^PG'
