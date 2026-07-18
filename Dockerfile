# ---- Stage 1: Build ----
FROM golang:1.26-alpine AS builder

RUN apk add --no-cache git

WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# Static binary, no CGO
ENV CGO_ENABLED=0
ENV GOOS=linux
ENV GOARCH=amd64

RUN go build -ldflags="-s -w" -o feedshit ./cmd/feedshit/

# ---- Stage 2: Runtime ----
FROM alpine:3.20

RUN apk add --no-cache ca-certificates tzdata \
    && addgroup -S appgroup \
    && adduser -S appuser -G appgroup

WORKDIR /app

COPY --from=builder /build/feedshit .

# Data directory for SQLite + uploads
RUN mkdir -p /app/data && chown -R appuser:appgroup /app/data

USER appuser

EXPOSE 8080

ENV DATA_DIR=/app/data
ENV PORT=8080

ENTRYPOINT ["./feedshit"]
