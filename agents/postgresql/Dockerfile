ARG VERSION=latest
ARG GO_VERSION=1.23

FROM golang:${GO_VERSION} AS build-go

COPY . /go/src/github.com/rclsilver-org/backup-controller
WORKDIR /go/src/github.com/rclsilver-org/backup-controller

RUN CGO_ENABLED=0 go build -o /usr/local/bin/backup-controller-postgresql-agent agents/postgresql/cmd/main.go

FROM ghcr.io/rclsilver-org/backup-controller-agent-default:${VERSION}

RUN apk add --update --no-cache postgresql-client

COPY --from=build-go /usr/local/bin/backup-controller-postgresql-agent /usr/local/bin/backup-controller-postgresql-agent

ENV BC_CMD='/usr/local/bin/backup-controller-postgresql-agent'
ENV EXTRA_VARS='^PG'
