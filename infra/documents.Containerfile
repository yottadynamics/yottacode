# yottacode-documents — production image for document subprocesses.
#
# Build locally:
#
#   podman build -t yottacode-documents -f infra/documents.Containerfile .
#
# Or use the GHCR image after the Documents image workflow has published it:
#
#   ghcr.io/yottadynamics/yottacode-documents:latest
#
# Then point yottacode's command sandbox at it:
#
#   [sandbox]
#   backend = "podman"
#   image   = "yottacode-documents"
#
# This image intentionally contains only the tools the current production
# document paths invoke:
#   - pandoc: create_document docx/pdf, and read_document docx rich tier
#   - weasyprint: create_document pdf engine
#   - poppler-utils: read_document pdf (pdftotext/pdfinfo)
#
# xlsx and pptx generation/parsing are native Go paths and do not use this
# image. LibreOffice and Python document libraries are deliberately omitted
# until a code path actually invokes them.
#
# Base image: Debian, not the UBI9 image config.DefaultSandboxImage uses for
# the general-purpose run_bash sandbox. UBI9's default repos do not carry
# pandoc; Debian ships the document subprocess stack directly via apt.
#
# TODO before relying on this beyond the first production publishing pass: pin
# the base image below to a specific digest instead of a floating tag.
FROM debian:bookworm-slim

RUN apt-get update && apt-get install -y --no-install-recommends \
    pandoc \
    poppler-utils \
    weasyprint \
    ca-certificates \
    && rm -rf /var/lib/apt/lists/*

# Sanity-check the toolchain at build time so a broken image fails the build
# instead of failing silently on first real use.
RUN pandoc --version \
    && pdftotext -v \
    && pdfinfo -v \
    && weasyprint --version

CMD ["sleep", "infinity"]
