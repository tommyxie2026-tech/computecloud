# syntax=docker/dockerfile:1.7

ARG GO_IMAGE=golang:1.27.1-alpine
ARG CERTS_IMAGE=alpine:3.22
ARG WORKER_IMAGE=debian:bookworm-slim

FROM --platform=$BUILDPLATFORM ${GO_IMAGE} AS build
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=0.0.0-dev
WORKDIR /src

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY . .
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -buildvcs=false \
      -ldflags "-s -w -X main.version=${VERSION}" \
      -o /out/computecloud ./cmd/computecloud

FROM ${CERTS_IMAGE} AS server-rootfs
RUN apk add --no-cache ca-certificates \
 && mkdir -p /rootfs/tmp /rootfs/var/lib/computecloud /rootfs/etc/computecloud \
 && chmod 1777 /rootfs/tmp \
 && chown -R 65532:65532 /rootfs/var/lib/computecloud

FROM scratch AS server
ARG VERSION=0.0.0-dev
ARG REVISION=unknown
ARG SOURCE=https://github.com/tommyxie2026-tech/computecloud
LABEL org.opencontainers.image.title="computecloud-server" \
      org.opencontainers.image.description="Minimal computecloud Agent Job Executor control plane image" \
      org.opencontainers.image.source="${SOURCE}" \
      org.opencontainers.image.revision="${REVISION}" \
      org.opencontainers.image.version="${VERSION}"
COPY --from=server-rootfs /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=server-rootfs /rootfs/ /
COPY --from=build --chown=65532:65532 /out/computecloud /computecloud
USER 65532:65532
ENV HOME=/var/lib/computecloud \
    SSL_CERT_FILE=/etc/ssl/certs/ca-certificates.crt
WORKDIR /var/lib/computecloud
ENTRYPOINT ["/computecloud"]
CMD ["server","--config","/etc/computecloud/computecloud.yaml"]

FROM ${WORKER_IMAGE} AS worker
ARG VERSION=0.0.0-dev
ARG REVISION=unknown
ARG SOURCE=https://github.com/tommyxie2026-tech/computecloud
LABEL org.opencontainers.image.title="computecloud-worker" \
      org.opencontainers.image.description="computecloud Worker base image with git/ssh and no bundled Agent runtime" \
      org.opencontainers.image.source="${SOURCE}" \
      org.opencontainers.image.revision="${REVISION}" \
      org.opencontainers.image.version="${VERSION}"
RUN apt-get update \
 && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
      ca-certificates git openssh-client \
 && rm -rf /var/lib/apt/lists/* \
 && mkdir -p /home/computecloud /var/lib/computecloud /etc/computecloud \
 && printf 'computecloud:x:65532:65532:computecloud:/home/computecloud:/bin/sh\n' >> /etc/passwd \
 && printf 'computecloud:x:65532:\n' >> /etc/group \
 && chown -R 65532:65532 /home/computecloud /var/lib/computecloud
COPY --from=build --chown=65532:65532 /out/computecloud /usr/local/bin/computecloud
USER 65532:65532
ENV HOME=/home/computecloud
WORKDIR /var/lib/computecloud
ENTRYPOINT ["/usr/local/bin/computecloud"]
CMD ["worker","--config","/etc/computecloud/computecloud.yaml"]
