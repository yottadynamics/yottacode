---
name: dockerfile-review
description: Review a Dockerfile (or Containerfile) for correctness, layer caching, multi-stage opportunities, image size, security, and reproducibility. Use when the user asks "review my Dockerfile", "why is my image so large?", or "is this Dockerfile good?".
license: MIT
metadata:
  author: yottacode
---

# Dockerfile review

Walk the Dockerfile top-to-bottom and apply this checklist. Cite line numbers in your review so the user can navigate to each finding.

## 1. Base image choice

- **Is the tag pinned?** `FROM alpine:latest` is a footgun — the image changes under you. Pin to a digest (`@sha256:...`) for reproducible builds, or at minimum a specific tag (`alpine:3.20`). Flag any `:latest`, `:edge`, or untagged base.
- **Is the size appropriate for the workload?** `python:3.12` is ~1 GB; `python:3.12-slim` is ~150 MB; `python:3.12-alpine` is ~50 MB but breaks on packages with C extensions that aren't built for musl. Suggest the lightest base that still works.
- **For Go / Rust / static binaries**: final stage should be `gcr.io/distroless/static-debian12` or `scratch`. Anything else is wasted bytes.
- **Multi-arch?** If the user runs on ARM (M-series Mac, AWS Graviton, RPi), the base image must support `linux/arm64`. `--platform=$BUILDPLATFORM` and a separate `--platform=$TARGETPLATFORM` final stage cover this.

## 2. Layer ordering — cache invalidation

Docker invalidates every layer downstream of the first changed line. Order matters.

Standard pattern, in this order:

```dockerfile
FROM ...
WORKDIR /app

# 1. System deps (rarely change)
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates && rm -rf /var/lib/apt/lists/*

# 2. Package manifests only — installs deps, cacheable
COPY package.json package-lock.json ./
RUN npm ci --omit=dev

# 3. Source — invalidates only on actual code changes
COPY . .

# 4. Build / final commands
RUN npm run build
```

Common smells to flag:

- `COPY . .` before installing deps — every code change re-runs the install.
- `RUN apt-get update` separate from `RUN apt-get install ...` — the cached `update` layer can serve a stale package list to the install.
- `RUN apt-get install` without `--no-install-recommends` and without cleaning `/var/lib/apt/lists/` in the same `RUN` — ships ~50 MB of recommends + apt cache.

## 3. Multi-stage opportunities

If the image carries compilers, package managers, or test tools at runtime, it should be multi-stage.

```dockerfile
FROM golang:1.23 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/app ./cmd/app

FROM gcr.io/distroless/static-debian12
COPY --from=build /out/app /app
ENTRYPOINT ["/app"]
```

Flag any of these as "should be multi-stage":
- Compiled languages with the compiler in the final image (gcc, go, rustc, javac).
- Node images with `node_modules/.cache`, dev deps, or `npm`/`yarn` in the runtime.
- Python images with `pip`, build tools, or `*.pyc` from tests.

## 4. Security

- **`USER root` at runtime** — every Dockerfile should `RUN useradd -r app && USER app` (or equivalent) before `CMD` / `ENTRYPOINT`. Flag if missing.
- **Secrets baked into layers** — `ENV API_KEY=...` or `ARG SECRET=...` followed by use in `RUN` makes the secret visible in `docker history`. Suggest `--secret` build args or runtime env vars.
- **`COPY` of sensitive files** — `.env`, private keys, SSH config. Check for a `.dockerignore` and verify it covers them.
- **`ADD` of remote URLs** — `ADD https://...` downloads at build time with no checksum verification. Suggest `RUN curl ... && sha256sum --check` or pin the URL to a digest.
- **`RUN curl | sh`** — pipes a remote script to a shell with no verification. Flag and suggest pinning.
- **Outdated packages** — if the base image is more than ~6 months stale, suggest a base bump.

## 5. Reproducibility

- `RUN pip install` (or `npm install`, etc.) **without a lockfile** is non-deterministic. Insist on `pip install -r requirements.txt --no-deps` with a fully-pinned `requirements.txt`, or equivalent for the ecosystem.
- `RUN apt-get install -y <pkg>` without a version pin will drift over time. For long-lived images, pin: `apt-get install -y pkg=1.2.3`.
- `--build-arg` values that change should be documented; they bust the cache from that line down.

## 6. Image-size pathologies

- **Multiple `RUN apt-get install` lines** — each creates a layer. Consolidate into one `RUN` chained with `&&` and ending with `rm -rf /var/lib/apt/lists/*`.
- **`COPY` of `.git/` directory** — typically 10-100+ MB of history nobody needs at runtime. Ensure `.dockerignore` covers `.git`.
- **`node_modules` in the build context** — must be in `.dockerignore`, or the local copy ships into the image.
- **Test fixtures, build artifacts, IDE files** — covered by a thorough `.dockerignore`.

## 7. Health and signals

- **`HEALTHCHECK`** absent — flag for any long-running service (web server, worker). Lack of a healthcheck breaks orchestrators that rely on it.
- **`CMD` vs `ENTRYPOINT`** — prefer `ENTRYPOINT ["/app"]` (exec form, signal-forwarding) over `CMD /app` (shell form, signals routed to /bin/sh).
- **`STOPSIGNAL`** — if the app needs something other than SIGTERM (rare), declare it.

## 8. Output format

Structure the review as:

1. **Critical** (must fix — security, broken build, runtime breakage)
2. **Strong recommendation** (size or cache wins ≥ 30%, missing healthcheck on a service, no `.dockerignore`)
3. **Nits** (minor cleanups, style)

Each finding is one line: `Line N — <what's wrong> — <suggested fix>`. The user can act on each independently.
