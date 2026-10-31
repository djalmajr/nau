# syntax=docker/dockerfile:1
#
# nau hub — container image (build context = the repository root):
#   docker build -t nau-hub:<tag> .
#
# Multi-stage: (1) web SPA with bun; (2) Go static binary;
# (3) distroless static. The hub serves the SPA itself with --web.
# The cluster runs the health probes on /api/health, so the image
# carries no HEALTHCHECK. Base images are pinned by exact tag and
# digest.

# ---- 1) Front (Vite/React) -------------------------------------------------
FROM docker.io/oven/bun:1.4.0@sha256:5ff609364c049b54eb0ff560ec96319729a972078ef2c755d758f0c6ef89c2d6 AS web
WORKDIR /web
COPY web/package.json web/bun.lock ./
RUN bun install --frozen-lockfile
COPY web/ ./
# vite outDir = dist → /web/dist
RUN bun run build

# ---- 2) Static Go binary ---------------------------------------------------
FROM docker.io/library/golang:1.26.0@sha256:fb612b7831d53a89cbc0aaa7855b69ad7b0caf603715860cf538df854d047b84 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ ./cmd/
COPY internal/ ./internal/
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X github.com/djalmajr/nau/internal/cli.version=${VERSION}" -o /out/nau ./cmd/nau

# ---- 3) Minimal runtime ----------------------------------------------------
FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab
COPY --from=build /out/nau /usr/local/bin/nau
COPY --from=web /web/dist /srv/web
USER nonroot
EXPOSE 7440
ENTRYPOINT ["/usr/local/bin/nau","hub","serve","--listen","0.0.0.0:7440","--web","/srv/web"]
