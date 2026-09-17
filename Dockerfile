# syntax=docker/dockerfile:1

# ---- web UI ----------------------------------------------------------------
# Built once on the build machine's architecture: the output is static assets.
FROM --platform=$BUILDPLATFORM node:24.21.0-trixie-slim AS web
WORKDIR /src/frontend
COPY frontend/package.json frontend/package-lock.json ./
RUN --mount=type=cache,target=/root/.npm npm ci --no-audit --no-fund
COPY frontend/ ./
RUN npm run build

# ---- build -------------------------------------------------------------
# Go cross-compiles, so the build stage also runs natively and targets
# $TARGETARCH. That keeps multi-architecture builds fast (no emulation).
FROM --platform=$BUILDPLATFORM golang:1.27.1-trixie AS build
WORKDIR /src
COPY backend/go.mod backend/go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY backend/ ./
# The web UI is embedded into the binary (internal/api/webui).
COPY --from=web /src/frontend/dist/ ./internal/api/webui/dist/
ARG VERSION=dev
ARG COMMIT=unknown
ARG TARGETOS
ARG TARGETARCH
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath \
      -ldflags="-s -w -X main.version=${VERSION} -X main.commit=${COMMIT}" \
      -o /out/syslogc ./cmd/syslogc && \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath -ldflags="-s -w" -o /out/loggen ./cmd/loggen

# ---- loggen (synthetic traffic generator) -------------------------------
FROM gcr.io/distroless/static-debian13:nonroot AS loggen
COPY --from=build /out/loggen /usr/local/bin/loggen
USER 65532:65532
ENTRYPOINT ["/usr/local/bin/loggen"]

# ---- syslogc (default target) --------------------------------------------
FROM gcr.io/distroless/static-debian13:nonroot AS syslogc
COPY --from=build /out/syslogc /usr/local/bin/syslogc
COPY deploy/docker/syslogc.yaml /etc/syslogc/syslogc.yaml
USER 65532:65532
# 8080: HTTP (health, readiness, metrics, API) · 5514: syslog UDP/TCP · 6514: syslog TLS
EXPOSE 8080 5514/udp 5514/tcp 6514/tcp
HEALTHCHECK --interval=10s --timeout=3s --start-period=5s --retries=3 \
  CMD ["/usr/local/bin/syslogc", "healthcheck"]
ENTRYPOINT ["/usr/local/bin/syslogc"]
CMD ["serve", "--config", "/etc/syslogc/syslogc.yaml"]
