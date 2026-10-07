# syntax=docker/dockerfile:1@sha256:4edf897a3ffa55b89f906fc8cc78afdb3f1834cc9c7083565e611a8a7d5fe99e
# (docker/dockerfile:1 pinned by digest; refresh with: docker buildx imagetools inspect docker/dockerfile:1)
# One image for local dev (dev container), CI and the libyang oracle.
#   ./dev make ci              everything CI runs
#   ./dev make oracle-check    lyoracle vs committed goldens
ARG GO_IMAGE=golang:1.27.1-trixie@sha256:433790e515d27dc6003e847e644cc0af956985cf315c1c58a3b73ee2dd305183
ARG DEBIAN_IMAGE=debian:trixie-slim@sha256:a99cfc517144bc59b1978475ec53b46ecabec7e43635402ee5b77cc54cd1b20a

FROM ${DEBIAN_IMAGE} AS libyang
ARG LIBYANG_REF=v5.8.6
ARG LIBYANG_COMMIT=47351e59e2965e350f9d4098f15ecbe6b3850f6f
ARG PCRE2_VERSION=10.46-1~deb13u3
RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      ca-certificates git gcc libc6-dev make cmake \
      libpcre2-dev=${PCRE2_VERSION} libpcre2-8-0=${PCRE2_VERSION} \
 && rm -rf /var/lib/apt/lists/*
# xxhash deliberately NOT installed (libyang falls back to its built-in hash).
RUN git clone --depth 1 --branch "${LIBYANG_REF}" https://github.com/CESNET/libyang.git /src/libyang \
 && test "$(git -C /src/libyang rev-parse HEAD)" = "${LIBYANG_COMMIT}" \
      || { echo "libyang ${LIBYANG_REF} is $(git -C /src/libyang rev-parse HEAD), expected ${LIBYANG_COMMIT}" >&2; exit 1; }
RUN cmake -S /src/libyang -B /src/build \
      -DCMAKE_BUILD_TYPE=Release \
      -DCMAKE_INSTALL_PREFIX=/opt/libyang \
      -DCMAKE_INSTALL_LIBDIR=lib \
      -DENABLE_TESTS=OFF -DENABLE_VALGRIND_TESTS=OFF \
      -DENABLE_TOOLS=ON -DENABLE_YANGLINT_INTERACTIVE=OFF \
 && cmake --build /src/build -j"$(nproc)" \
 && cmake --install /src/build \
 && { echo "libyang ${LIBYANG_REF} ${LIBYANG_COMMIT}"; \
      echo "pcre2 $(dpkg-query -W -f='${Version}' libpcre2-dev)"; \
      echo "gcc $(gcc -dumpfullversion)"; echo "cmake $(cmake --version | head -1)"; \
      grep -E '^CMAKE_(BUILD_TYPE|C_FLAGS[A-Z_]*):' /src/build/CMakeCache.txt; \
      grep -E '^(XXHASH|PCRE2)[A-Z_]*:' /src/build/CMakeCache.txt; } > /opt/libyang/BUILDINFO \
 && cat /opt/libyang/BUILDINFO
# libyang's test modules and fuzz corpus: the parser oracle test compares on them (BSD-3-Clause, © CESNET).
# The image ships libyang (library, yanglint, these sources), so it ships libyang's LICENSE and a
# NOTICE for the third-party YANG modules among them. PCRE2's copyright comes with its Debian
# package (/usr/share/doc/libpcre2-*/copyright).
RUN mkdir -p /opt/libyang/src /opt/libyang/share/doc/libyang \
 && cp -r /src/libyang/tests /src/libyang/modules /opt/libyang/src/ \
 && cp /src/libyang/LICENSE /opt/libyang/share/doc/libyang/LICENSE \
 && printf '%s\n' \
      "libyang ${LIBYANG_REF} (${LIBYANG_COMMIT}), https://github.com/CESNET/libyang" \
      "" \
      "/opt/libyang (library, headers, yanglint, src/tests, src/modules) is libyang, distributed" \
      "under the BSD 3-Clause License in LICENSE next to this file (Copyright (c) CESNET)." \
      "" \
      "The IETF and IANA YANG modules under /opt/libyang/share/yang and /opt/libyang/src (ietf-*.yang," \
      "iana-*.yang) are IETF Trust material; each carries its copyright notice and is distributed" \
      "under the Revised BSD License of the IETF Trust's Legal Provisions Relating to IETF Documents" \
      "(https://trustee.ietf.org/license-info), as stated in the module text." \
      > /opt/libyang/share/doc/libyang/NOTICE

FROM ${GO_IMAGE} AS dev
# Links the published ghcr.io/vibe-ports/yang-dev package to this repository.
LABEL org.opencontainers.image.source="https://github.com/vibe-ports/yang" \
      org.opencontainers.image.description="Dev, CI and libyang-oracle image for vibe-ports/yang"
ARG PCRE2_VERSION=10.46-1~deb13u3
ARG GOLANGCI_LINT_VERSION=v2.14.0
ARG GOVULNCHECK_VERSION=v1.8.0
ARG GITLEAKS_VERSION=v8.30.1
ARG ACTIONLINT_VERSION=v1.7.12
ARG ZIZMOR_VERSION=v1.30.1
ARG ZIZMOR_SHA256_AMD64=e65324f4430c2717591937edcec90ccbefaf14c174f8ec9415e03ca875b46e1a
ARG ZIZMOR_SHA256_ARM64=7ff1dce33bdd18fd2a4affe63bdd47efcccca97b2cec1c1863ec26e9e2647540
ARG TARGETARCH
RUN apt-get update && apt-get install -y --no-install-recommends \
      libpcre2-dev=${PCRE2_VERSION} libpcre2-8-0=${PCRE2_VERSION} sudo jq universal-ctags cscope \
    && rm -rf /var/lib/apt/lists/*
COPY --from=libyang /opt/libyang /opt/libyang
ENV PATH=/opt/libyang/bin:${PATH} \
    LD_LIBRARY_PATH=/opt/libyang/lib \
    PKG_CONFIG_PATH=/opt/libyang/lib/pkgconfig \
    GOTOOLCHAIN=local \
    GOPATH=/home/dev/go \
    GOCACHE=/home/dev/.cache/go-build
# Go tools via go install: module hashes are checked against the Go checksum database.
RUN GOBIN=/usr/local/bin go install "github.com/golangci/golangci-lint/v2/cmd/golangci-lint@${GOLANGCI_LINT_VERSION}" \
    && GOBIN=/usr/local/bin go install "golang.org/x/vuln/cmd/govulncheck@${GOVULNCHECK_VERSION}" \
    && GOBIN=/usr/local/bin go install "github.com/zricethezav/gitleaks/v8@${GITLEAKS_VERSION}" \
    && GOBIN=/usr/local/bin go install "github.com/rhysd/actionlint/cmd/actionlint@${ACTIONLINT_VERSION}"
# zizmor (Rust): release tarball pinned by sha256.
RUN case "${TARGETARCH}" in \
      amd64) arch=x86_64 sum="${ZIZMOR_SHA256_AMD64}" ;; \
      arm64) arch=aarch64 sum="${ZIZMOR_SHA256_ARM64}" ;; \
      *) echo "zizmor: unsupported arch ${TARGETARCH}" >&2; exit 1 ;; \
    esac \
    && curl -sSfL -o /tmp/zizmor.tgz "https://github.com/zizmorcore/zizmor/releases/download/${ZIZMOR_VERSION}/zizmor-${arch}-unknown-linux-gnu.tar.gz" \
    && echo "${sum}  /tmp/zizmor.tgz" | sha256sum -c - \
    && tar -xzf /tmp/zizmor.tgz -C /usr/local/bin zizmor \
    && rm /tmp/zizmor.tgz
# Non-root user; all Go caches live under its home so devcontainer UID remapping
# (which chowns the home dir) keeps them writable.
RUN useradd -m -u 1000 -s /bin/bash dev && echo 'dev ALL=(ALL) NOPASSWD:ALL' > /etc/sudoers.d/dev \
    && mkdir -p /home/dev/go/pkg/mod /home/dev/.cache && chown -R dev:dev /home/dev
USER dev
# Bind-mounted checkout is owned by the host uid; let git (vcs stamping, gitleaks) read it.
RUN git config --global --add safe.directory '*'
WORKDIR /workspaces/yang
