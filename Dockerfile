# syntax=docker/dockerfile:1
#
# Multi-arch runtime image for headless Asimi execution (e.g. Cloud Run Jobs).
#
# Expected Cloud Run / ADC runtime environment contract:
#   ASIMI_PROVIDER=vertex           LLM provider (Vertex AI)
#   ASIMI_MODEL=gemini-4-neon       Model identifier
#   GOOGLE_CLOUD_PROJECT            GCP project ID for Vertex AI
#   GOOGLE_CLOUD_REGION=global      Optional Vertex AI region
#   GOOGLE_APPLICATION_CREDENTIALS  Unset (uses Cloud Run ADC via metadata server)
#   ASIMI_HOME=/tmp/asimi           Ephemeral writable directory for SQLite & logs
#
# The webhook event arrives as a CLI argument in the online service's
# delivery format, supplied by the Cloud Run Job's Args (the ENTRYPOINT
# above stays unchanged):
#   asimi --isolated-host --handsoff --event '{"provider":"github","event_type":"push","delivery_id":"...","received_at":"...","payload":{...}}'

FROM --platform=$BUILDPLATFORM golang:1.26.10-bookworm AS builder

ARG TARGETOS
ARG TARGETARCH

WORKDIR /src
COPY . .

RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-amd64} \
    go build -mod=vendor -tags containers_image_openpgp -ldflags="-s -w" \
    -o /out/asimi .

FROM debian:bookworm-slim

LABEL org.opencontainers.image.source="https://github.com/afittestide/asimi" \
      org.opencontainers.image.description="Asimi headless coding agent runtime for Cloud Run and isolated-host execution" \
      org.opencontainers.image.licenses="MIT"

RUN apt-get update && \
    apt-get install -y --no-install-recommends ca-certificates git curl && \
    rm -rf /var/lib/apt/lists/* && \
    useradd --create-home --shell /bin/bash asimi && \
    mkdir -p /workspace /tmp/asimi && \
    chown -R asimi:asimi /workspace /tmp/asimi

COPY --from=builder /out/asimi /usr/local/bin/asimi

ENV ASIMI_HOME=/tmp/asimi
WORKDIR /workspace
USER asimi

ENTRYPOINT ["asimi", "--isolated-host", "--handsoff"]
