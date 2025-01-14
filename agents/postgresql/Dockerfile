ARG VERSION=latest

FROM ghcr.io/rclsilver-org/backup-controller-agent-default:${VERSION}

RUN apk add --update --no-cache postgresql-client

COPY . /opt/backup-controller/scripts

ENV BC_CMD='/opt/backup-controller/scripts/postgresql-command.sh'
ENV EXTRA_VARS='^PG'
