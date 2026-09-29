# yang

A pure-Go YANG 1.1 library — schema parsing and compilation, a generic data tree,
RFC 7950 validation, RFC 7951 JSON and XML encoding, diff — built as an
**AI-assisted port of [libyang](https://github.com/CESNET/libyang)** (v5.8.6).

## Goals

1. **No cgo.** Go projects that bind libyang through cgo today should be able to
   drop the C toolchain, libyang and libpcre2: `go get`, static binaries,
   cross-compilation, a plain `go test -race`.
2. **Compatibility with libyang.** For the same schemas and data, the result must
   match libyang: accept/reject, validation verdict, diagnostic code and data path,
   and the JSON/XML/diff output. libyang is the test oracle, pinned to one release.
3. **Auditability.** The code is translated from libyang's C sources by AI coding
   agents (Anthropic Claude, OpenAI Codex), so every step has to be traceable:
   - every ported Go file names the libyang file and release it comes from;
   - every oracle case is reproducible by anyone with the pinned oracle container;
   - every intentional difference from libyang is listed with its RFC
     justification in `conformance/deviations.md`;
   - the compatibility report (agreeing cases / total, per area) is published.

## Status

M0 (foundations): plan, design notes, libyang oracle, XSD-regex compiler. See [PLAN.md](PLAN.md)
and how this compares with other Go YANG projects: [docs/comparison.md](docs/comparison.md).

## License

BSD-3-Clause. Portions are derived from libyang, © CESNET, BSD-3-Clause — see
[LICENSE](LICENSE). Not affiliated with or endorsed by CESNET or the libyang
authors.
