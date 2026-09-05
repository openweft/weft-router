# weft-router production OCI image.
#
# Two-stage build : a Go build stage produces a statically-linked
# binary, then we copy it into a scratch base. ~ 16 MB image,
# no shell, no package manager — the binary IS the daemon.
#
# Consumed by openweft's lifecycle path : weft-network calls
# `weft microvm run ghcr.io/openweft/weft-router:<tag>` (or its API
# equivalent) per Router resource it manages.
#
# Multi-arch : buildx sets TARGETOS / TARGETARCH which we forward to
# `go build`. Publish covers linux/amd64 + linux/arm64 + linux/riscv64
# + linux/loong64 — the build stage is pinned to --platform=$BUILDPLATFORM
# (the runner's own native arch) rather than the target one, so it
# cross-compiles instead of running the Go toolchain itself under QEMU
# emulation for every target; the scratch final stage has no OS content
# of its own, so it never needs a base-image manifest for the target
# platform either. This is why loong64 works here even though the
# official golang image publishes no linux/loong64 manifest at all.
#
# Build args :
#   - VERSION : git describe output, stamped via -ldflags.
#   - COMMIT  : short sha.
#   - DATE    : RFC-3339 UTC build timestamp.
#
# Local build :
#   docker buildx build --platform linux/amd64,linux/arm64,linux/riscv64,linux/loong64 \
#     --build-arg VERSION=$(git describe --tags --always --dirty) \
#     --build-arg COMMIT=$(git rev-parse --short HEAD) \
#     --build-arg DATE=$(date -u +%Y-%m-%dT%H:%M:%SZ) \
#     -t ghcr.io/openweft/weft-router:dev .

ARG GO_VERSION=1.26

# ---- build stage --------------------------------------------------
FROM --platform=$BUILDPLATFORM golang:${GO_VERSION}-alpine AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY cmd/ ./cmd/
COPY internal/ ./internal/

ARG VERSION=dev
ARG COMMIT=none
ARG DATE=unknown
ARG TARGETOS
ARG TARGETARCH

RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build \
      -trimpath \
      -ldflags "-s -w \
                -X main.version=${VERSION} \
                -X main.commit=${COMMIT} \
                -X main.date=${DATE}" \
      -o /out/weft-router \
      ./cmd/weft-router

# ---- runtime stage ------------------------------------------------
FROM scratch
COPY --from=build /out/weft-router /weft-router

# /metrics defaults to :9100 inside the container ; override via
# --metrics-addr at run time. BGP listener (:179) is the data-plane
# concern, exposed only when the operator actually runs this as a
# privileged micro-VM.
EXPOSE 9100
ENTRYPOINT ["/weft-router"]
CMD ["agent"]
