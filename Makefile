# All targets are meant to run inside the dev container (./dev make <target>).
FUZZTIME ?= 60s

.PHONY: ci test-oracle fmt-check vet lint nocgo test test-386 vuln secrets sensitive fuzz oracle test-go-min oracle-check oracle-golden

ci: fmt-check vet lint nocgo test test-386 test-go-min test-oracle vuln secrets sensitive oracle-check

fmt-check:
	@out=$$(git ls-files -z '*.go' | xargs -0 -r gofmt -l); [ -z "$$out" ] || { echo "gofmt needed:"; echo "$$out"; exit 1; }
	go mod tidy -diff

vet:
	go vet ./...

lint:
	golangci-lint run
	actionlint -shellcheck= .github/workflows/*.yml

# Production code must never use cgo (goal 1). The oracle lives outside this module.
nocgo:
	@f=$$(grep -rl --include='*.go' --exclude-dir=conformance --exclude-dir=spike --exclude-dir=.git 'import "C"' . || true); [ -z "$$f" ] || { echo "import \"C\" in: $$f"; exit 1; }
	@pk=$$(CGO_ENABLED=1 go list -deps -f '{{if and (not .Standard) .CgoFiles}}{{.ImportPath}}{{end}}' ./...); [ -z "$$pk" ] || { echo "cgo in dependency closure: $$pk"; exit 1; }
	CGO_ENABLED=0 go build ./...

test:
	go test -race -shuffle=on -coverprofile=coverage.out ./...

# 32-bit run catches int-width assumptions (ranges, decimal64).
test-386:
	GOARCH=386 CGO_ENABLED=0 go test ./...

# Oldest supported Go (go.mod `go` line) — the directive alone doesn't stop newer stdlib API use.
GO_MIN ?= go1.26.8
test-go-min:
	GOTOOLCHAIN=$(GO_MIN) go test ./...

# Differential tests against the pinned libyang (build tag `oracle`); must not skip in CI.
test-oracle:
	YANG_ORACLE_REQUIRED=1 go test -tags oracle ./...

vuln:
	govulncheck ./...

# Secret scan over the whole git history.
secrets:
	gitleaks git --no-banner --redact .

# Private details (infra, personal, employer, local paths). Personal patterns come from the
# SENSITIVE_PATTERNS env (CI secret) or ~/.config/vibe-ports/denylist, never from the repo.
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

oracle-check: oracle
	python3 conformance/oracle/run_corpus.py --check

oracle-golden: oracle
	python3 conformance/oracle/run_corpus.py

# libyang v5.8.6 sources for porting and reviews (host side, gitignored).
libyang-src:
	[ -d .cache/libyang ] || git clone -q --depth 1 --branch v5.8.6 https://github.com/CESNET/libyang .cache/libyang
