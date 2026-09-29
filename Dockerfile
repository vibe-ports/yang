# syntax=docker/dockerfile:1
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

FROM ${GO_IMAGE} AS dev
ARG PCRE2_VERSION=10.46-1~deb13u3
ARG GOLANGCI_LINT_VERSION=v2.14.0
ARG GOVULNCHECK_VERSION=v1.8.0
ARG GITLEAKS_VERSION=v8.30.1
ARG ACTIONLINT_VERSION=v1.7.12
RUN apt-get update && apt-get install -y --no-install-recommends \
      libpcre2-dev=${PCRE2_VERSION} libpcre2-8-0=${PCRE2_VERSION} python3-yaml sudo \
    && rm -rf /var/lib/apt/lists/*
COPY --from=libyang /opt/libyang /opt/libyang
ENV PATH=/opt/libyang/bin:${PATH} \
    LD_LIBRARY_PATH=/opt/libyang/lib \
    PKG_CONFIG_PATH=/opt/libyang/lib/pkgconfig \
    GOTOOLCHAIN=local \
    GOPATH=/home/dev/go \
    GOCACHE=/home/dev/.cache/go-build
RUN curl -sSfL "https://raw.githubusercontent.com/golangci/golangci-lint/${GOLANGCI_LINT_VERSION}/install.sh" \
      | sh -s -- -b /usr/local/bin "${GOLANGCI_LINT_VERSION}" \
    && GOBIN=/usr/local/bin go install "golang.org/x/vuln/cmd/govulncheck@${GOVULNCHECK_VERSION}" \
    && GOBIN=/usr/local/bin go install "github.com/zricethezav/gitleaks/v8@${GITLEAKS_VERSION}" \
    && GOBIN=/usr/local/bin go install "github.com/rhysd/actionlint/cmd/actionlint@${ACTIONLINT_VERSION}"
# Non-root user; all Go caches live under its home so devcontainer UID remapping
# (which chowns the home dir) keeps them writable.
RUN useradd -m -u 1000 -s /bin/bash dev && echo 'dev ALL=(ALL) NOPASSWD:ALL' > /etc/sudoers.d/dev \
    && mkdir -p /home/dev/go/pkg/mod /home/dev/.cache && chown -R dev:dev /home/dev
USER dev
# Bind-mounted checkout is owned by the host uid; let git (vcs stamping, gitleaks) read it.
RUN git config --global --add safe.directory '*'
WORKDIR /workspaces/yang
