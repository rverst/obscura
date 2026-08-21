# syntax=docker/dockerfile:1

# obscura builds without cgo, which makes cross-compilation free: the builder
# always runs on the native platform and simply emits a binary for TARGETARCH.
# Building for a Raspberry Pi on an x86 laptop therefore needs no emulation and
# no cross toolchain.

# ---------- dependencies ----------------------------------------------------
FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS deps
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

# ---------- test ------------------------------------------------------------
# Runs on the BUILD platform, never cross-compiled: cross-compiled test binaries
# could not be executed here without emulation.
FROM deps AS test
COPY . .
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
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" \
    -o /out/obscurad ./cmd/obscurad

# ---------- runtime ---------------------------------------------------------
# Alpine rather than scratch: M2 will need ffmpeg for video poster frames, and
# adding it to an existing base is a one-line change. wget (busybox) also gives
# us a healthcheck without baking one into the binary.
FROM alpine:3.22 AS runtime

RUN apk add --no-cache ca-certificates tzdata && \
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
