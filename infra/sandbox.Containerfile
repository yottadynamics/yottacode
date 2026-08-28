# yottacode-sandbox — default command sandbox image for run_bash/run_tests.
#
# Build locally:
#
#   podman build -t yottacode-sandbox -f infra/sandbox.Containerfile .
#
# Or use the GHCR image after the Sandbox image workflow has published it:
#
#   ghcr.io/yottadynamics/yottacode-sandbox:latest
#
# This image is intentionally a general-purpose engineering baseline, not a
# language-server or document-processing image. It includes the Go toolchain and
# common POSIX/build tools needed by yottacode's own `go test`/`go vet` flows.
# Project-specific stacks should still layer their own image and point
# `[sandbox].image` at it.
#
# Base image: UBI 9.8, matching yottacode's sandbox hardening baseline. The Go
# toolchain is copied from the official Go image so the version can track go.mod
# exactly instead of depending on whatever UBI repos currently package.
ARG GO_VERSION=1.26.6
ARG UBI_IMAGE=registry.access.redhat.com/ubi9/ubi:9.8-1785906690

FROM docker.io/library/golang:${GO_VERSION} AS go-toolchain

FROM ${UBI_IMAGE}

COPY --from=go-toolchain /usr/local/go /usr/local/go
ENV PATH="/usr/local/go/bin:${PATH}"

# Keep this baseline broad enough for normal Go projects without turning it into
# a kitchen-sink devcontainer. gcc/glibc-devel are included because cgo is on by
# default for many linux/amd64 Go toolchains and some dependency tests require a
# working C compiler.
RUN dnf -y install \
    ca-certificates \
    diffutils \
    findutils \
    gcc \
    git \
    glibc-devel \
    gzip \
    make \
    patch \
    tar \
    unzip \
    xz \
    && dnf clean all \
    && rm -rf /var/cache/dnf

# Build-time smoke catches broken tags or PATH mistakes before CI publishes the
# image. Runtime smoke in .github/workflows/sandbox-image.yml exercises a mounted
# checkout through Podman, which is the production shape yottacode uses.
RUN go version && git --version && make --version

CMD ["sleep", "infinity"]
