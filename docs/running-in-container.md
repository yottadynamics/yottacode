# Running yottacode in a Container

This guide covers running yottacode inside a container for full OS-level isolation. The container approach jails **the entire agent** — TUI, file tools, shell commands, everything — which is the simplest and strongest isolation model.

> **TL;DR** For a quick start with Podman:
> ```bash
> podman build -t yottacode -f Containerfile .
> podman run -it --rm -v "$(pwd):/workspace" -v "$HOME/.yottacode:/home/yottacode/.yottacode" yottacode
> ```

---

## Containerfile

Create a `Containerfile` (or `Dockerfile`) in your project root or home directory:

```dockerfile
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
```

---

## Quick Start

### 1. Build the image

```bash
# From the yottacode repo root (or any directory with the Containerfile)
podman build -t yottacode -f Containerfile .
```

### 2. Run interactively (TUI)

```bash
podman run -it --rm \
  -v "$(pwd):/workspace" \
  -v "$HOME/.yottacode:/home/yottacode/.yottacode" \
  yottacode
```

This mounts:
- **Current directory** → `/workspace` (your project files)
- **Host config dir** → `/home/yottacode/.yottacode` (API keys, memory, sessions, permissions)

### 3. Run one-shot (non-interactive)

```bash
podman run --rm \
  -v "$(pwd):/workspace" \
  -v "$HOME/.yottacode:/home/yottacode/.yottacode" \
  yottacode run "Summarize the main.go file"
```

---

## Common Scenarios

### A. Persistent config on host, ephemeral container

Best for daily use — your API keys, memory, and sessions persist on the host.

```bash
podman run -it --rm \
  -v "$(pwd):/workspace" \
  -v "$HOME/.yottacode:/home/yottacode/.yottacode" \
  yottacode
```

### B. Fully self-contained (config inside container)

Useful for CI, throwaway environments, or testing.

```bash
podman run -it --rm \
  -v "$(pwd):/workspace" \
  yottacode
```
On first run, `yottacode setup` launches inside the container and writes config to the container's `/home/yottacode/.yottacode`. Data is lost when container exits.

### C. Devcontainer (VS Code / Cursor / JetBrains)

Create `.devcontainer/devcontainer.json` in your project:

```json
{
  "name": "yottacode",
  "build": { "dockerfile": "Containerfile" },
  "mounts": [
    "source=${localWorkspaceFolder},target=/workspace,type=bind",
    "source=${env:HOME}/.yottacode,target=/home/yottacode/.yottacode,type=bind"
  ],
  "runArgs": ["--user=1000:1000"],
  "customizations": {
    "vscode": {
      "extensions": [],
      "settings": { "terminal.integrated.defaultProfile.linux": "bash" }
    }
  }
}
```

Then: **Dev Containers: Reopen in Container** → `yottacode` runs natively inside the devcontainer terminal.

### D. With GPU access (for local models via Ollama)

```bash
podman run -it --rm \
  --device nvidia.com/gpu=all \
  -v "$(pwd):/workspace" \
  -v "$HOME/.yottacode:/home/yottacode/.yottacode" \
  -v /run/user/$(id -u)/ollama:/run/user/1000/ollama \
  yottacode
```
Requires NVIDIA Container Toolkit and `podman machine` with GPU support on macOS.

### E. Rootless Podman on macOS (via podman machine)

```bash
# One-time setup
podman machine init --cpus 4 --memory 8g --disk-size 50g
podman machine start

# Then run as normal (mounts work via virtiofs)
podman run -it --rm \
  -v "$(pwd):/workspace" \
  -v "$HOME/.yottacode:/home/yottacode/.yottacode" \
  yottacode
```

---

## Mounting Additional Host Paths

| Need | Mount |
|------|-------|
| SSH keys for git | `-v "$HOME/.ssh:/home/yottacode/.ssh:ro"` |
| GitHub CLI auth | `-v "$HOME/.config/gh:/home/yottacode/.config/gh:ro"` |
| AWS credentials | `-v "$HOME/.aws:/home/yottacode/.aws:ro"` |
| Docker socket (for `docker` commands inside) | `-v /var/run/docker.sock:/var/run/docker.sock` |
| Local Ollama API | `--add-host=host.docker.internal:host-gateway` + `-v /run/user/$(id -u)/ollama:/run/user/1000/ollama` |

> **Security note**: Only mount what you need. Each mount expands the trust boundary.

---

## Environment Variables

Pass provider keys at runtime instead of baking into the image:

```bash
podman run -it --rm \
  -v "$(pwd):/workspace" \
  -v "$HOME/.yottacode:/home/yottacode/.yottacode" \
  -e ANTHROPIC_API_KEY \
  -e OPENAI_API_KEY \
  -e GITHUB_TOKEN \
  yottacode
```

Or use an `.env` file:
```bash
podman run -it --rm --env-file .env \
  -v "$(pwd):/workspace" \
  -v "$HOME/.yottacode:/home/yottacode/.yottacode" \
  yottacode
```

---

## Building from a Tag (No Local Source)

If you don't have the repo cloned, build directly from GitHub:

```bash
# Build from a specific tag
podman build -t yottacode:v0.2.0 \
  https://github.com/yottadynamics/yottacode.git#v0.2.0:Containerfile

# Or use the multi-arch release binary (faster, no Go toolchain needed)
# See: https://github.com/yottadynamics/yottacode/releases
```

---

## Troubleshooting

### "Permission denied" on mounted files
The container runs as UID 1000. If your host UID differs:
```bash
# Option 1: Run as your host UID
podman run -it --rm --user $(id -u):$(id -g) \
  -v "$(pwd):/workspace" \
  -v "$HOME/.yottacode:/home/yottacode/.yottacode" \
  yottacode

# Option 2: Fix ownership inside container (one-time)
podman run -it --rm -u root \
  -v "$(pwd):/workspace" \
  -v "$HOME/.yottacode:/home/yottacode/.yottacode" \
  yottacode chown -R 1000:1000 /home/yottacode/.yottacode /workspace
```

### TUI doesn't render / colors broken
Ensure terminal capabilities are passed:
```bash
podman run -it --rm \
  -e TERM=$TERM \
  -e COLORTERM=$COLORTERM \
  -v "$(pwd):/workspace" \
  -v "$HOME/.yottacode:/home/yottacode/.yottacode" \
  yottacode
```

### "no space left on device" (Podman machine)
```bash
podman machine stop
podman machine rm
podman machine init --disk-size 100g
podman machine start
```

### Container can't reach host services (localhost)
Use `host.containers.internal` (Podman) or `host.docker.internal` (Docker) instead of `localhost`:
```bash
# For Ollama on host
-e OLLAMA_HOST=http://host.containers.internal:11434
```

---

## Security Model

Running yottacode in a container provides:

| Layer | Protection |
|-------|------------|
| **Filesystem** | Only mounted paths visible (default: project + config) |
| **Network** | Default CNI network; add `--network=none` for full deny |
| **Process** | Separate PID namespace; can't see host processes |
| **User** | Runs as non-root (UID 1000) inside container |
| **Capabilities** | Default rootless Podman drops most capabilities |

To harden further:
```bash
podman run -it --rm \
  --network=none \
  --read-only \
  --tmpfs /tmp --tmpfs /home/yottacode/.cache \
  --cap-drop=ALL \
  --security-opt=no-new-privileges \
  -v "$(pwd):/workspace" \
  -v "$HOME/.yottacode:/home/yottacode/.yottacode" \
  yottacode
```

---

## Comparison: Container vs. Podman Command Sandbox

| Aspect | Whole Binary in Container | Podman Command Sandbox (planned) |
|--------|---------------------------|----------------------------------|
| **Isolation scope** | Entire agent (TUI, tools, commands) | Only `run_bash` commands |
| **TUI latency** | Slight overhead (PTY over container) | Native host speed |
| **File tool speed** | Mount overhead | Native host speed |
| **LSP / editor integration** | Works via devcontainer | Native |
| **Setup complexity** | One `podman run` | Config flag + Podman installed |
| **Status** | Works today | Future opt-in feature |

**Recommendation**: Use the container approach today for maximum isolation. The Podman command sandbox (when shipped) will be an additional opt-in layer for users who want host-speed TUI with jailed shell commands.

---

## See Also

- [Security and Allow Lists](security-and-allow-lists.md) — permissions model
- [FAQ](faq.md#does-yottacode-sandbox-commands) — sandbox FAQ
- [Configuration](configuration.md#for-real-isolation-run-yottacode-inside-a-container-or-devcontainer) — config reference
- [Architecture](architecture.md) — safety layers overview