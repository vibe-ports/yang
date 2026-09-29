# All targets are meant to run inside the dev container (./dev make <target>).
FUZZTIME ?= 60s

.PHONY: ci fmt-check vet lint nocgo test test-386 vuln secrets fuzz oracle oracle-check oracle-golden

ci: fmt-check vet lint nocgo test test-386 vuln secrets oracle-check

fmt-check:
	@out=$$(gofmt -l .); [ -z "$$out" ] || { echo "gofmt needed:"; echo "$$out"; exit 1; }
	go mod tidy -diff

vet:
	go vet ./...

lint:
	golangci-lint run

# Production code must never use cgo (goal 1). The oracle lives outside this module.
nocgo:
	@pk=$$(go list -f '{{if .CgoFiles}}{{.ImportPath}}{{end}}' ./...); [ -z "$$pk" ] || { echo "cgo used in: $$pk"; exit 1; }
	CGO_ENABLED=0 go build ./...

test:
	go test -race -shuffle=on -coverprofile=coverage.out ./...

# 32-bit run catches int-width assumptions (ranges, decimal64).
test-386:
	GOARCH=386 CGO_ENABLED=0 go test ./...

vuln:
	govulncheck ./...

# Secret scan over the whole git history.
secrets:
	gitleaks git --no-banner --redact .

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
