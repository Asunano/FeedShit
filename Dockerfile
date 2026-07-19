# ---- Stage 1: Build ----
# Use `make docker` to build with pinned digests (see Makefile).
ARG GO_IMAGE=golang:1.26-alpine
FROM ${GO_IMAGE} AS builder

# VERSION is injected at build time (e.g. `docker build --build-arg VERSION=1.2.3`).
ARG VERSION=dev

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
ARG ALPINE_IMAGE=alpine:3.20
FROM ${ALPINE_IMAGE}

ARG VERSION=dev
LABEL org.opencontainers.image.title="FeedShit" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.description="Lightweight multi-project feedback collection system"

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

# Tag the image with an immutable version (commit SHA / release tag).
# Build + tag example:
#   docker build --build-arg VERSION=1.2.3 -t feedshit:1.2.3 .
# Never ship a bare `latest` to production — it is not traceable.
ENTRYPOINT ["./feedshit"]
