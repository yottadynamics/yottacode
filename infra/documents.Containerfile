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
#   - pdfplumber (pip): read_document's PDF table-extraction tier
#     (internal/documents/pyhelpers/extract_pdf_tables.py)
#   - python-docx (pip): create_document's docx template-fill path
#     (internal/documents/pyhelpers/fill_docx_template.py)
#
# python3 itself is not installed explicitly above — weasyprint already
# pulls it in transitively via its own apt dependency chain (confirmed by
# inspecting a real build: python3-pil, python3-numpy, python3-scipy, etc.
# install as weasyprint dependencies). pdfplumber/python-docx are not
# packaged for Debian bookworm (unlike pandoc/weasyprint/poppler-utils),
# so unlike everything else in this image they come from pip with pinned
# versions, not apt.
#
# xlsx and pptx generation/parsing are native Go paths and do not use this
# image. LibreOffice is deliberately omitted until a code path actually
# invokes it.
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
    python3-pip \
    ca-certificates \
    && rm -rf /var/lib/apt/lists/*

# --break-system-packages: Debian bookworm's python3-pip enforces PEP 668
# externally-managed-environment protection by default. This image has no
# other pip-managed system tooling to conflict with, and there's no
# meaningful "system Python workflow" to protect in a single-purpose
# container — exactly the case that flag exists for. Versions pinned so a
# rebuild doesn't silently pick up a breaking upstream release.
RUN pip install --no-cache-dir --break-system-packages \
    pdfplumber==0.11.10 \
    python-docx==1.2.0

# The driver scripts' canonical source lives at
# internal/documents/pyhelpers/*.py, embedded into the yottacode binary
# itself via go:embed for the host-fallback path (no sandbox configured)
# — this COPY is the *other* half of the same source, baked into the
# image for the podman-sandbox path. Same files, two delivery mechanisms,
# never duplicated by hand.
COPY internal/documents/pyhelpers/*.py /opt/yottacode/doc-helpers/

# Sanity-check the toolchain at build time so a broken image fails the build
# instead of failing silently on first real use.
RUN pandoc --version \
    && pdftotext -v \
    && pdfinfo -v \
    && weasyprint --version \
    && python3 -c "import pdfplumber, docx" \
    && python3 -m py_compile /opt/yottacode/doc-helpers/*.py

CMD ["sleep", "infinity"]
