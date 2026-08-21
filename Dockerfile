# syntax=docker/dockerfile:1

# obscura builds without cgo, which makes cross-compilation free: the builder
# always runs on the native platform and simply emits a binary for TARGETARCH.
# Building for a Raspberry Pi on an x86 laptop therefore needs no emulation and
# no cross toolchain.

# ---------- frontend --------------------------------------------------------
# Runs on the BUILD platform: the output is static files, identical for every
# target architecture, so there is nothing to gain from emulating the target.
FROM --platform=$BUILDPLATFORM node:24-alpine AS web
WORKDIR /web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ .
# Type check and unit tests run here, in the stage that already has the
# toolchain. Leaving them out of the image build would mean the authoritative
# pipeline never runs them.
RUN npm run test
RUN npm run build

# ---------- dependencies ----------------------------------------------------
FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS deps
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

# ---------- test ------------------------------------------------------------
# Runs on the BUILD platform, never cross-compiled: cross-compiled test binaries
# could not be executed here without emulation.
FROM deps AS test
# The thumbnailer shells out to vipsthumbnail, so without it those tests would
# silently skip and the suite would look green while covering nothing.
RUN apk add --no-cache vips-tools
COPY . .
COPY --from=web /web/dist ./web/dist
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    gofmt -l ./cmd ./internal | tee /tmp/unformatted && \
    test ! -s /tmp/unformatted
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go vet ./...
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go test -count=1 ./...

# ---------- build -----------------------------------------------------------
FROM deps AS build
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
COPY . .
# Overwrites the committed dist/.gitkeep placeholder with the real build, which
# is what go:embed then bakes into the binary.
COPY --from=web /web/dist ./web/dist
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" \
    -o /out/obscurad ./cmd/obscurad

# ---------- runtime ---------------------------------------------------------
# Alpine rather than scratch: obscura shells out to vipsthumbnail and ffmpeg, so
# the runtime needs a package manager rather than an empty filesystem. busybox
# wget also serves as a healthcheck without baking one into the binary.
FROM alpine:3.22 AS runtime

# vips-tools provides vipsthumbnail, which obscura runs as a subprocess rather
# than linking through cgo -- that is what keeps the build cgo-free and the
# arm64 cross-build a single command.
#
# ffmpeg is deliberately NOT installed yet. It is needed for video poster frames
# in M5, and adding it costs 78MB on top of this image -- almost all of it
# encoder libraries (libavcodec, x265, aom, SVT-AV1, spirv-tools) that obscura
# will never invoke, because it does not transcode. When M5 lands, check whether
# a decode-only build can be used instead of pulling the whole codec stack onto
# an SD card.
RUN apk add --no-cache ca-certificates tzdata vips-tools && \
    adduser -D -u 10001 -h /data obscura

COPY --from=build /out/obscurad /usr/local/bin/obscurad

# The data directory holds auth.db, photos.db and the encrypted blobs. Mount a
# volume here -- without one, every container restart discards the library.
RUN mkdir -p /data && chown -R obscura:obscura /data && chmod 700 /data
VOLUME ["/data"]

USER obscura
WORKDIR /data
EXPOSE 8080

HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
    CMD wget -qO- http://127.0.0.1:8080/api/status >/dev/null || exit 1

ENTRYPOINT ["/usr/local/bin/obscurad"]
# 0.0.0.0 rather than the binary's 127.0.0.1 default: inside a container,
# loopback would not be reachable from outside.
CMD ["-addr", "0.0.0.0:8080", "-data", "/data"]
