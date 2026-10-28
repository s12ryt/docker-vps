# syntax=docker/dockerfile:1

# ---------- build stage: cross-compile webshell for the target arch ----------
# BUILDPLATFORM keeps the compiler running natively (fast, no QEMU for Go),
# while TARGETARCH selects the output binary architecture.
FROM --platform=$BUILDPLATFORM golang:1.26-bookworm AS build
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src

# dependency layer first for optimal build cache hits
COPY webshell/go.mod webshell/go.sum ./
RUN go mod download

COPY webshell/ ./
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags='-s -w' -o /out/webshell .

# ---------- runtime stage ----------
FROM debian:bookworm-slim

# base tools expected from a slim VPS-style image
RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      bash \
      ca-certificates \
      curl \
      vim \
      git \
      less \
      procps \
      net-tools \
      iproute2 \
      openssh-client \
      tmux \
      htop \
      tini \
 && rm -rf /var/lib/apt/lists/*

# webshell + tini live OUTSIDE the persisted directory tree: when the
# persistent layer is active, /usr on the container root is replaced by a
# symlink into the volume, which would shadow binaries placed under /usr.
# /app is never migrated, so the freshly-built webshell always wins.
RUN mkdir -p /app && cp /usr/bin/tini /app/tini
COPY --from=build /out/webshell /app/webshell

ENV PORT=7681 \
    SHELL=/bin/bash

EXPOSE 7681

# start-period is generous: first boot with a mounted /data copies ~600MB
# (the whole /usr tree) into the volume before the shell is ready.
HEALTHCHECK --interval=30s --timeout=3s --start-period=5m --retries=3 \
  CMD curl -fsS "http://127.0.0.1:${PORT}/healthz" || exit 1

# tini reaps orphaned shell processes and forwards signals
ENTRYPOINT ["/app/tini", "--", "/app/webshell"]
