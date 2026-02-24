# slack-bridge: standalone beads→Slack notification bridge.
# Multi-stage build: Go builder → distroless runtime.
#
# Build:
#   docker build -t gasboat/slack-bridge:latest images/slack-bridge/ \
#     --build-arg VERSION=$(git describe --tags --always)

FROM golang:1.25-bookworm AS builder

ARG VERSION=dev
ARG COMMIT=unknown

WORKDIR /build
COPY controller/ ./

RUN CGO_ENABLED=0 go build \
    -ldflags="-s -w -X main.version=${VERSION} -X main.commit=${COMMIT}" \
    -o /slack-bridge ./cmd/slack-bridge/

# ── Runtime ─────────────────────────────────────────────────────────
FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=builder /slack-bridge /slack-bridge

USER nonroot:nonroot
EXPOSE 8090

ENTRYPOINT ["/slack-bridge"]
