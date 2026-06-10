# Containerfile — yottacode container image
# Build:  podman build -t yottacode -f Containerfile .
# Run:    podman run -it --rm -v "$(pwd):/workspace" -v "$HOME/.yottacode:/home/yottacode/.yottacode" yottacode

# --- Base image ------------------------------------------------------------
FROM docker.io/library/debian:stable-slim AS base

# Install runtime dependencies: ca-certs for TLS, git for version control,
# sudo for optional privilege escalation, and a terminal-capable shell.
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    git \
    sudo \
    bash \
    && rm -rf /var/lib/apt/lists/*

# --- Builder ---------------------------------------------------------------
FROM docker.io/library/golang:1.26 AS builder

WORKDIR /src
# Cache go.mod/go.sum for faster builds
COPY go.mod go.sum ./
RUN go mod download

# Copy source and build static binary
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
    -trimpath -ldflags="-s -w" -o /out/yottacode ./cmd/yottacode

# --- Final image -----------------------------------------------------------
FROM base

# Create non-root user (UID 1000 matches typical host user)
RUN groupadd -g 1000 yottacode && \
    useradd -u 1000 -g yottacode -m -s /bin/bash yottacode && \
    echo "yottacode ALL=(ALL) NOPASSWD:ALL" >> /etc/sudoers

# Copy binary from builder
COPY --from=builder /out/yottacode /usr/local/bin/yottacode

# Verify binary works
RUN yottacode version

# Runtime config
ENV HOME=/home/yottacode
ENV YOTTACODE_CONFIG_DIR=/home/yottacode/.yottacode
WORKDIR /workspace

USER yottacode
ENTRYPOINT ["yottacode"]