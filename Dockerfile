# syntax=docker/dockerfile:1

# ---------------------------------------------------------------------------
# Build stage
# ---------------------------------------------------------------------------
FROM golang:1.27-alpine AS builder

WORKDIR /src

RUN apk add --no-cache ca-certificates tzdata git

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath -ldflags="-s -w" -o /out/stayin-api ./cmd/api

# ---------------------------------------------------------------------------
# Runtime stage
# ---------------------------------------------------------------------------
FROM alpine:3.21

RUN apk add --no-cache ca-certificates tzdata wget \
    && addgroup -g 10001 -S app \
    && adduser -u 10001 -S app -G app

COPY --from=builder /out/stayin-api /usr/local/bin/stayin-api

# The bind mount for /DATA/Media overrides this directory at runtime. It exists
# so the image is runnable on its own and so ownership is predictable.
RUN mkdir -p /DATA/Media && chown -R 10001:10001 /DATA

USER 10001:10001

EXPOSE 8080

HEALTHCHECK --interval=30s --timeout=5s --start-period=20s --retries=3 \
    CMD wget -qO- http://127.0.0.1:8080/healthz || exit 1

ENTRYPOINT ["/usr/local/bin/stayin-api"]
