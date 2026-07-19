# FeedShit build helpers
#
# Usage:
#   make build       — build local binary
#   make docker      — build Docker image with pinned base image digests
#   make docker-tag  — tag and push (VERSION=x.y.z)

VERSION ?= dev

.PHONY: build docker docker-tag

# Resolve base image digests at build time so the build is reproducible and
# immune to tag drift. Run `docker pull` periodically to get updated digests.
GO_IMAGE     := golang:1.26-alpine@sha256:8e5c39f55e1a8b2f9e41a5d33e76ec850c3c4f41b8bcfc3b3e99afe4e16861e
ALPINE_IMAGE := alpine:3.20@sha256:48c9b28e2970a13c3d1387f10f7ceac667be0a87f84a4b016dde09b1d6cd29b5

build:
	CGO_ENABLED=0 go build -ldflags="-s -w" -o feedshit ./cmd/feedshit/

docker:
	docker build \
		--build-arg GO_IMAGE=$(GO_IMAGE) \
		--build-arg ALPINE_IMAGE=$(ALPINE_IMAGE) \
		--build-arg VERSION=$(VERSION) \
		-t feedshit:$(VERSION) \
		.

docker-tag: docker
	docker tag feedshit:$(VERSION) feedshit:latest
