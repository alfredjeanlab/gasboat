# wl-bridge: standalone beads→wasteland sync bridge.
# Multi-stage build: Go builder → debian runtime (needs dolt CLI).
#
# Build:
#   docker build -t gasboat/wl-bridge:latest -f images/wl-bridge/Dockerfile \
#     --build-arg VERSION=$(git describe --tags --always) .

FROM golang:1.25-bookworm AS builder

ARG VERSION=dev
ARG COMMIT=unknown

WORKDIR /build
COPY controller/ ./

RUN CGO_ENABLED=0 go build \
    -ldflags="-s -w -X main.version=${VERSION} -X main.commit=${COMMIT}" \
    -o /wl-bridge ./cmd/wl-bridge/

# Install dolt CLI.
RUN curl -fsSL https://github.com/dolthub/dolt/releases/latest/download/install.sh | bash

# ── Runtime ─────────────────────────────────────────────────────────
# Use debian-slim instead of distroless because wl-bridge shells out to dolt.
FROM debian:bookworm-slim

RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates && rm -rf /var/lib/apt/lists/*

COPY --from=builder /wl-bridge /wl-bridge
COPY --from=builder /usr/local/bin/dolt /usr/local/bin/dolt

RUN useradd -r -u 65534 -s /usr/sbin/nologin nonroot
USER nonroot
EXPOSE 8092

ENTRYPOINT ["/wl-bridge"]
