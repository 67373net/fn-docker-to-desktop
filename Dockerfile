# Build Stage
FROM golang:alpine AS builder

WORKDIR /build

# Pre-copy go.mod for caching layer
COPY go.mod ./

# Copy source files
COPY cmd/ ./cmd/
COPY internal/ ./internal/
COPY web/ ./web/

# Compile with static linking and strip debug symbols for minimal binary size
# Mount compiler cache for lightning-fast rebuilds
RUN --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux GOTOOLCHAIN=local GOPROXY=off \
    go build -trimpath -ldflags="-s -w" -o /build/put-port-on-desktop ./cmd/server

# Final Stage
FROM alpine:latest

WORKDIR /app

# Copy binary and product icon
COPY --from=builder /build/put-port-on-desktop /app/put-port-on-desktop
COPY icon.png /app/icon.png

VOLUME ["/app/data"]

ENTRYPOINT ["/app/put-port-on-desktop"]
