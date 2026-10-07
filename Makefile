# All targets are meant to run inside the dev container (./dev make <target>).
FUZZTIME ?= 60s

.PHONY: ci mod-verify test-oracle fmt-check vet lint nocgo test test-386 vuln secrets sensitive fuzz oracle test-go-min oracle-check oracle-golden test-gates registries port-coverage compat-report

ci: mod-verify fmt-check vet lint nocgo test test-386 test-go-min test-oracle test-gates vuln secrets sensitive oracle-check registries

# Security gates (check-sensitive, hooks, merge-pr, ai-review publisher, review-tier) and the
# agent task queue (scripts/claim) against stubbed gh/curl and throwaway repos.
test-gates:
	scripts/test-gates
	scripts/test-claim
	scripts/test-lyfn

# Duplicate ids/rows left by the union merge driver in the append-only registries.
registries:
	scripts/check-registries

# Module cache contents match go.sum.
mod-verify:
	go mod verify
	cd conformance && go mod verify

fmt-check:
	@out=$$(git ls-files -z '*.go' | xargs -0 -r gofmt -l); [ -z "$$out" ] || { echo "gofmt needed:"; echo "$$out"; exit 1; }
	go mod tidy -diff
	cd conformance && go mod tidy -diff

vet:
	go vet ./...
	cd conformance && go vet ./...

lint:
	golangci-lint run
	cd conformance && golangci-lint run
	actionlint -shellcheck= .github/workflows/*.yml
	zizmor --offline --no-progress .github/workflows

# Production code must never use cgo (goal 1). The oracle lives outside this module.
nocgo:
	@f=$$(grep -rl --include='*.go' --exclude-dir=conformance --exclude-dir=spike --exclude-dir=.git 'import "C"' . || true); [ -z "$$f" ] || { echo "import \"C\" in: $$f"; exit 1; }
	@pk=$$(CGO_ENABLED=1 go list -deps -f '{{if and (not .Standard) .CgoFiles}}{{.ImportPath}}{{end}}' ./...); [ -z "$$pk" ] || { echo "cgo in dependency closure: $$pk"; exit 1; }
	CGO_ENABLED=0 go build ./...

test:
	go test -race -shuffle=on -coverprofile=coverage.out ./...
	cd conformance && go test -race -shuffle=on ./...

# 32-bit run catches int-width assumptions (ranges, decimal64).
test-386:
	GOARCH=386 CGO_ENABLED=0 go test ./...
	cd conformance && GOARCH=386 CGO_ENABLED=0 go test ./...

# Oldest supported Go (go.mod `go` line) — the directive alone doesn't stop newer stdlib API use.
GO_MIN ?= go1.26.8
test-go-min:
	GOTOOLCHAIN=$(GO_MIN) go test ./...
	cd conformance && GOTOOLCHAIN=$(GO_MIN) go test ./...

# Differential tests against the pinned libyang (build tag `oracle`); must not skip in CI.
test-oracle:
	YANG_ORACLE_REQUIRED=1 go test -tags oracle ./...

vuln:
	govulncheck ./...
	cd conformance && govulncheck ./...

# Secret scan over the whole git history.
secrets:
	gitleaks git --no-banner --redact .

# Private details (infra, personal, employer, local paths). Generic rules here and in CI; the
# personal patterns (~/.config/vibe-ports/denylist) exist only on the maintainer's host and are
# applied by the git hooks — run scripts/check-sensitive on the host for the full check.
sensitive:
	scripts/check-sensitive --all
	scripts/check-sensitive --history

# Runs every Fuzz target for FUZZTIME each.
fuzz:
	@for p in $$(go list ./...); do \
	  for f in $$(go test -list '^Fuzz' $$p | grep '^Fuzz' || true); do \
	    echo "== $$p $$f"; go test -run '^$$' -fuzz "^$$f$$" -fuzztime $(FUZZTIME) $$p || exit 1; \
	  done; \
	done

# libyang oracle (C, test-only). LIBYANG_PREFIX=/opt/libyang inside the dev container.
LIBYANG_PREFIX ?= /opt/libyang
oracle:
	$(MAKE) -C conformance/oracle LIBYANG_PREFIX=$(LIBYANG_PREFIX)

# libyang prints date-and-time in the host's local zone: every oracle run is pinned to UTC.
test-oracle oracle-check oracle-golden: export TZ = UTC

oracle-check:
	$(MAKE) oracle
	cd conformance && go run ./cmd/golden -check -require-protocol && go test ./...

oracle-golden: oracle
	cd conformance && go run ./cmd/golden -require-protocol && go test ./...

# Port coverage (issue #77): libyang functions reachable from the pilot entry points that
# docs/port-map.md does not list; roots and out-of-scope areas in conformance/port-coverage.conf.
# Informational (CI job summary); runs on the host too (needs universal-ctags).
port-coverage: libyang-src
	@scripts/lyfn -n ly_ctx_new >/dev/null
	@cd conformance && go run ./cmd/portcov -src ../.cache/libyang -portmap ../docs/port-map.md -config port-coverage.conf

# Compatibility report: every corpus fixture run through this port and compared with the committed
# libyang goldens (agree / agree with skipped fields / differ / deviation / unsupported). Needs no
# oracle. Informational (CI job summary), like port-coverage.
compat-report:
	@cd conformance && go run ./cmd/report -engine yang

# libyang v5.8.6 sources for porting and reviews (host side, gitignored).
libyang-src:
	@[ -d .cache/libyang ] || git -c advice.detachedHead=false clone -q --depth 1 --branch v5.8.6 https://github.com/CESNET/libyang .cache/libyang
