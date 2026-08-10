# yottacode-documents — reference image for document generation/parsing.
#
# NOT published anywhere yet: build and use it locally. There is no CI
# workflow pushing this to a registry (see roadmap/document-generation.md's
# "Sandbox integration" section for why that's a deliberate scope decision,
# not an oversight).
#
#   podman build -t yottacode-documents -f infra/documents.Containerfile .
#
# Then point yottacode's command sandbox at it:
#
#   [sandbox]
#   backend = "podman"
#   image   = "yottacode-documents"
#
# and enable both the sandbox and document_generation experimental flags.
# See docs/document-generation.md for the full setup and docs/sandbox.md
# for how the command sandbox itself works.
#
# Contents cover the full roadmap/document-generation.md "documents" image
# spec (pandoc, headless LibreOffice, poppler-utils, python3 + doc
# libraries) even though today's create_document tool only invokes pandoc
# for docx/pdf generation — the rest (LibreOffice recalculation/conversion,
# PDF table extraction, docx/pptx generation-from-scratch) is unused by
# this round's Go code but already staged for the parsing (Phase B/C) and
# Python-helper (Phase 4) rounds that come later, so this image doesn't
# need a rebuild when those land.
#
# TODO before relying on this in anything but a local/dev setting: pin the
# base image below to a specific digest rather than a pinned-by-tag build,
# per roadmap/document-generation.md's "no floating latest for deps" build
# requirement. The tag matches config.DefaultSandboxImage, so this reference
# image starts from the same UBI baseline as yottacode's default sandbox.
FROM registry.access.redhat.com/ubi9/ubi:9.8-1785906690

# Keep the RPM layer to platform packages that are generally available from
# UBI/AppStream, then install the Python document libraries with pip. The
# image is intentionally broad because later document parsing/generation
# phases can reuse it without changing the sandbox base.
RUN dnf -y update \
    && dnf -y install --setopt=install_weak_deps=False \
    pandoc \
    libreoffice \
    poppler-utils \
    python3 \
    python3-pip \
    ca-certificates \
    && dnf clean all \
    && rm -rf /var/cache/dnf

# This is a single-purpose document-tooling container, not a general Python
# environment, so a system-level pip install is acceptable here. weasyprint
# is installed from PyPI so create_document's PDF path can use it as pandoc's
# PDF engine even when the distro package set doesn't carry it.
RUN pip3 install --no-cache-dir \
    pdfplumber \
    pypdf \
    python-docx \
    python-pptx \
    reportlab \
    weasyprint

# Sanity-check the toolchain at build time so a broken image fails the
# build instead of failing silently on first real use.
RUN pandoc --version \
    && soffice --version \
    && pdftotext -v \
    && weasyprint --version \
    && python3 -c "import pdfplumber, pypdf, docx, pptx, reportlab"

CMD ["sleep", "infinity"]
