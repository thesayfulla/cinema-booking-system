# Build stage: compile a static binary so the runtime image needs no toolchain.
FROM golang:1.26-alpine AS build

WORKDIR /src

# Dependencies first, so a source-only change reuses the cached module layer.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=linux go build \
        -trimpath \
        -ldflags="-s -w -X main.version=${VERSION}" \
        -o /out/api ./cmd

# Runtime stage.
FROM alpine:3.21

# ca-certificates is needed to reach a payment provider over TLS.
RUN apk add --no-cache ca-certificates tzdata wget && \
    adduser -D -u 10001 app

WORKDIR /app

COPY --from=build /out/api /app/api
COPY static /app/static

# Run unprivileged: nothing in this service needs root.
USER app

EXPOSE 8080

# The orchestrator gets a real signal about dependency health, not just liveness.
HEALTHCHECK --interval=15s --timeout=3s --start-period=10s --retries=3 \
    CMD wget -qO- http://127.0.0.1:8080/readyz >/dev/null || exit 1

ENTRYPOINT ["/app/api"]
