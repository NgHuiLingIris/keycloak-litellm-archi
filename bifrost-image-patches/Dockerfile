# syntax=docker/dockerfile:1.7

ARG BIFROST_BASE_IMAGE=maximhq/bifrost:v1.5.0

# Build the React/Vite UI from the vendored source with local patch overlays.
FROM node:25-alpine3.23@sha256:cf38e1f3c28ac9d81cdc0c51d8220320b3b618780e44ef96a39f76f7dbfef023 AS ui-builder
WORKDIR /app

COPY vendor/bifrost/ui/package*.json ./
RUN npm ci

COPY vendor/bifrost/ui/ ./
COPY bifrost-image-patches/bifrost/ui/ ./
RUN npm run build-enterprise

# Build the Go server from the vendored source with local patch overlays.
FROM golang:1.26.2-alpine3.23@sha256:f85330846cde1e57ca9ec309382da3b8e6ae3ab943d2739500e08c86393a21b1 AS builder
WORKDIR /build

RUN apk add --no-cache gcc musl-dev sqlite-dev binutils binutils-gold

ENV CGO_ENABLED=1 GOOS=linux

COPY vendor/bifrost/core/ ./core/
COPY vendor/bifrost/framework/ ./framework/
COPY vendor/bifrost/plugins/ ./plugins/
COPY vendor/bifrost/transports/ ./transports/
COPY bifrost-image-patches/bifrost/transports/ ./transports/

RUN go work init && \
  go work use ./core && \
  go work use ./framework && \
  go work use ./plugins/compat && \
  go work use ./plugins/governance && \
  go work use ./plugins/jsonparser && \
  go work use ./plugins/logging && \
  go work use ./plugins/maxim && \
  go work use ./plugins/mocker && \
  go work use ./plugins/otel && \
  go work use ./plugins/prompts && \
  go work use ./plugins/semanticcache && \
  go work use ./plugins/telemetry && \
  go work use ./transports

COPY --from=ui-builder /app/out ./transports/bifrost-http/ui

ARG VERSION=1.5.0-sso-topup
RUN cd /build/transports && \
  go build \
    -ldflags="-w -s -X main.Version=v${VERSION} -extldflags '-static'" \
    -a -trimpath \
    -tags "sqlite_static" \
    -o /app/main \
    ./bifrost-http

RUN test -f /app/main

# Keep the official v1.5.0 image as the runtime base, but replace the binary
# with the patched build compiled above.
FROM ${BIFROST_BASE_IMAGE} AS runtime
WORKDIR /app

COPY --from=builder /app/main /app/main
