# Build stage. The patch release is pinned for reproducible container builds;
# go.mod declares the minimum language/toolchain requirement.
FROM golang:1.26.5-alpine3.24 AS builder

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download && go mod verify

COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/rate-limiter ./cmd/server

# Runtime stage
FROM alpine:3.24

RUN apk --no-cache add ca-certificates \
    && addgroup -S app \
    && adduser -S app -G app

WORKDIR /app
COPY --from=builder /out/rate-limiter .
COPY config.yaml .

USER app
EXPOSE 8080

HEALTHCHECK --interval=30s --timeout=3s --start-period=5s \
    CMD wget -qO- "http://127.0.0.1:${PORT:-8080}/health" || exit 1

ENTRYPOINT ["/app/rate-limiter"]
