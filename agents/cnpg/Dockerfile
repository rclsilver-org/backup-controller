ARG VERSION=latest
ARG GO_VERSION=1.25

FROM golang:${GO_VERSION} AS build-go

COPY . /go/src/github.com/rclsilver-org/backup-controller
WORKDIR /go/src/github.com/rclsilver-org/backup-controller

RUN CGO_ENABLED=0 go build -o /usr/local/bin/backup-controller-cnpg-agent agents/cnpg/cmd/main.go

FROM ghcr.io/rclsilver-org/backup-controller-agent-default:${VERSION}

COPY --from=build-go /usr/local/bin/backup-controller-cnpg-agent /usr/local/bin/backup-controller-cnpg-agent

ENTRYPOINT ["/usr/local/bin/backup-controller-cnpg-agent"]
