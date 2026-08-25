# Build stage. The builder runs on the native platform of the build host and
# cross-compiles for the requested target, which keeps multi-architecture builds
# fast without emulation.
FROM --platform=$BUILDPLATFORM golang:1.22-alpine AS build

ARG TARGETOS
ARG TARGETARCH

ENV CGO_ENABLED=0 \
    GOTOOLCHAIN=local

WORKDIR /src

# Dependencies first so that a source-only change reuses the module layer.
COPY go.mod go.sum ./
RUN go mod download

COPY cmd ./cmd
COPY internal ./internal
COPY migrations ./migrations

# The SQLite driver is pure Go, so the same source cross-compiles to every target
# architecture without a C toolchain.
RUN GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w" -o /out/heartbridge-server ./cmd/server

# Runtime stage.
FROM alpine:3.20

# wget is used by the container health check; ca-certificates keeps outbound TLS
# usable for a future notification gateway.
RUN apk add --no-cache ca-certificates wget \
    && addgroup -S heartbridge \
    && adduser -S -G heartbridge heartbridge

WORKDIR /app

COPY --from=build /out/heartbridge-server /app/heartbridge-server

# The database lives on a volume so that state survives a container replacement.
RUN mkdir -p /app/data && chown -R heartbridge:heartbridge /app
VOLUME ["/app/data"]

ENV HEARTBRIDGE_HTTP_ADDR=":8080" \
    HEARTBRIDGE_DB_PATH="/app/data/heartbridge.sqlite" \
    HEARTBRIDGE_ENV="container" \
    HEARTBRIDGE_LOG_LEVEL="info" \
    GOTOOLCHAIN=local

USER heartbridge
EXPOSE 8080

HEALTHCHECK --interval=15s --timeout=3s --start-period=5s --retries=3 \
    CMD wget --quiet --tries=1 --spider http://127.0.0.1:8080/readyz || exit 1

ENTRYPOINT ["/app/heartbridge-server"]
