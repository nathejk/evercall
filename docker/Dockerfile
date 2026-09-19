# syntax=docker/dockerfile:1

# Multistage build for the evercall service.
#
#   golang ──► api-dev ──► base ──► build ──┐
#                                           ▼
#                                alpine ──► prod   (last stage = default target)
#
# `prod` MUST stay the final stage: .github/workflows/build-and-publish.yml
# builds this file with no `--target`, so Docker picks the last one.
# Build context is the repo root, not go/.

# ---------------------------------------------------------------------------
# api-dev — Go toolchain for the dev container (hot reload needs inotifywait)
# ---------------------------------------------------------------------------
FROM golang:1.24 AS api-dev

RUN apt-get update \
    && apt-get install -y --no-install-recommends inotify-tools \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app

# Keep the build cache inside the `api:/go` volume so the dev loop and tools
# aren't recompiled on every container start.
ENV GOCACHE=/go/.cache/go-build

# ---------------------------------------------------------------------------
# base — the image docker-compose runs in development
# ---------------------------------------------------------------------------
FROM api-dev AS base

# go.sum is matched by a glob so it stays optional: the module currently has no
# third-party dependencies, and COPY fails on a missing literal path.
COPY go/go.mod go/go.sum* ./
RUN go mod download

COPY go/ ./

# ---------------------------------------------------------------------------
# build — gate on tests, then produce the static binary
# ---------------------------------------------------------------------------
FROM base AS build

ARG GIT_COMMIT=unknown
ARG GIT_BRANCH=unknown
ARG BUILD_NUMBER=0
ARG BUILD_VERSION=dev

# CI runs no separate test step, so a red test or a vet failure must fail the
# image build. Add `go tool staticcheck ./...` here once it is registered as a
# tool directive in go.mod.
RUN go vet ./... \
    && go test -timeout 60s ./...

# CGO_ENABLED=0 gives a genuinely static binary with no libc dependency, which
# is what lets the prod stage be bare alpine. The module is stdlib-only, so
# there is nothing that needs cgo.
ENV CGO_ENABLED=0
RUN go build \
    -trimpath \
    -ldflags "-w -s \
      -X github.com/nathejk/evercall/internal/vcs.Version=${BUILD_VERSION} \
      -X github.com/nathejk/evercall/internal/vcs.Commit=${GIT_COMMIT} \
      -X github.com/nathejk/evercall/internal/vcs.Branch=${GIT_BRANCH} \
      -X github.com/nathejk/evercall/internal/vcs.BuildNumber=${BUILD_NUMBER}" \
    -o /evercall ./cmd/evercall

# ---------------------------------------------------------------------------
# prod — the published image (keep last)
# ---------------------------------------------------------------------------
FROM alpine:3.20 AS prod

ARG BUILD_VERSION=dev

# ca-certificates is needed for outbound HTTPS to the Evercall REST API; tzdata
# so timestamps in call events resolve local time correctly.
RUN apk add --no-cache ca-certificates tzdata

COPY --from=build /evercall /evercall
COPY docker/bin/init /init

# Port 8080, not 80: the service runs as an unprivileged user, which cannot bind
# a port below 1024. Traefik therefore needs
# `loadbalancer.server.port=8080` on this service.
ENV PORT=8080 \
    BUILD_VERSION=${BUILD_VERSION}

EXPOSE 8080

RUN adduser -D -H -u 10001 evercall
USER evercall

ENTRYPOINT ["/init"]
