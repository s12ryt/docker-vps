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

# ---------- translate-layer stage: build libdvps-t.so for the TARGET arch ----------
# Compiled natively under the target platform (buildx QEMU-emulates foreign
# archs): a native gcc toolchain is simpler and more reliable than
# cross-compiling the preload library.
FROM debian:bookworm-slim AS tlib
RUN apt-get update \
 && apt-get install -y --no-install-recommends gcc libc6-dev \
 && rm -rf /var/lib/apt/lists/*
WORKDIR /src
COPY webshell/translate/preload.c ./
RUN mkdir -p /out \
 && gcc -shared -fPIC -O2 -Wall -o /out/libdvps-t.so preload.c -ldl

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

# webshell + tini live OUTSIDE the rewritten tree (/app is not in the
# translate-layer whitelist): the supervisor must stay on the image layer
# so a fresh image always replaces it, and static binaries are unaffected
# by ld.so.preload anyway.
RUN mkdir -p /app && cp /usr/bin/tini /app/tini
COPY --from=build /out/webshell /app/webshell

# translate layer: global LD_PRELOAD path-rewrite library. Every dynamic
# program (apt, dpkg, user shells...) gets /usr /var /etc ... rewritten to
# /data/rootfs/... when the seeded marker exists; static binaries and the
# build-time RUNs below (file writes only) are unaffected.
COPY --from=tlib /out/libdvps-t.so /usr/lib/libdvps-t.so
RUN chmod 755 /usr/lib/libdvps-t.so \
 && echo '/usr/lib/libdvps-t.so' > /etc/ld.so.preload \
 && chmod 644 /etc/ld.so.preload

ENV PORT=7681 \
    SHELL=/bin/bash \
    LD_LIBRARY_PATH=/data/rootfs/usr/lib/x86_64-linux-gnu:/data/rootfs/usr/lib/x86_64-linux-gnu/systemd:/data/rootfs/usr/lib

EXPOSE 7681

# start-period is generous: first boot with a mounted /data copies ~600MB
# (the whole /usr tree) into the volume before the shell is ready.
HEALTHCHECK --interval=30s --timeout=3s --start-period=5m --retries=3 \
  CMD curl -fsS "http://127.0.0.1:${PORT}/healthz" || exit 1

# tini reaps orphaned shell processes and forwards signals
ENTRYPOINT ["/app/tini", "--", "/app/webshell"]
