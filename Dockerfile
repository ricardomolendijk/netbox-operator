# Build the manager binary.
#
# --platform=$BUILDPLATFORM pins this stage to the *builder's* architecture and lets the
# cross-compile below produce the target one. Without it, `buildx --platform linux/arm64`
# runs the whole Go toolchain under QEMU on an amd64 runner, which is minutes of emulation
# to produce a statically linked binary GOARCH could have produced natively.
FROM --platform=$BUILDPLATFORM golang:1.27 AS builder
ARG TARGETOS
ARG TARGETARCH

WORKDIR /workspace
# Copy the module files first so dependency download is cached independently of source.
COPY go.mod go.mod
COPY go.sum go.sum
RUN go mod download

COPY cmd/ cmd/
COPY api/ api/
COPY internal/ internal/

# CGO_ENABLED=0 so the binary runs on distroless static.
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} \
    go build -a -ldflags="-s -w" -o manager ./cmd/manager

# Distroless static: no shell, no package manager, nonroot by default.
FROM gcr.io/distroless/static:nonroot
WORKDIR /
COPY --from=builder /workspace/manager .
USER 65532:65532

ENTRYPOINT ["/manager"]
