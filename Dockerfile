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
# proot powers the /data translation layer (persistent guest rootfs without
# privileged mounts — works on docker cloud hosts that only expose /data).
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
      proot \
 && rm -rf /var/lib/apt/lists/*

COPY --from=build /out/webshell /usr/local/bin/webshell

ENV PORT=7681 \
    SHELL=/bin/bash

EXPOSE 7681

HEALTHCHECK --interval=30s --timeout=3s --start-period=2s --retries=3 \
  CMD curl -fsS "http://127.0.0.1:${PORT}/healthz" || exit 1

# tini reaps orphaned shell processes and forwards signals
ENTRYPOINT ["/usr/bin/tini", "--", "/usr/local/bin/webshell"]
