# yang

A pure-Go YANG library — an AI-assisted source port of
[libyang](https://github.com/CESNET/libyang) **v5.8.6**, with no cgo — that aims to give the
same verdicts, diagnostics and output as libyang for the same schemas and data.

The code is translated from libyang's C sources by AI coding agents (Anthropic Claude, OpenAI
Codex) and checked against libyang itself: every intentional difference is listed, every ported
function is traceable to its C source, and agreement is measured on a fixture corpus, not assumed.

## Status

Pre-release (v0). The schema side and datastore data are usable and measured; operations, anydata
payloads and diff/merge are not done yet.

| Area | Status | Notes |
|---|---|---|
| YANG 1.0 / 1.1 parser (modules, submodules, import/include with revision-date) | supported | YANG text only |
| Schema compilation: typedef chains, grouping/uses/refine, augment, choice/case, if-feature, identities, status, must/when/leafref XPath checks | supported | |
| Built-in types, unions, decimal64, `ietf-yang-types` / `ietf-inet-types` canonical forms | supported | two plugin types store as their base type (U-0010) |
| `pattern` (XSD regular expressions) | supported | compiled to RE2 through an XSD-regex compiler; intentional differences D-0002…D-0008 |
| Extension instances | partial | metadata (RFC 7952) and NACM are compiled; schema-mount, `yang-data`/`structure`, OpenConfig `regexp-posix` make `Load` fail (U-0023…U-0025) |
| `deviation` statements | **unsupported** | an implemented module with deviations fails with `ErrUnsupported` (U-0020); planned |
| Datastore data: JSON (RFC 7951) and XML parse, print, unknown-node policy (reject / skip / opaque) | supported | |
| Validation: types, leafref, instance-identifier, mandatory, min/max-elements, unique, must, when (with auto-delete), choice/case, NMDA operational mode | supported | libyang's diagnostics: LY_ERR / LY_VECODE names, data path, error-app-tag |
| Defaults and with-defaults printing (RFC 6243: explicit, trim, report-all, report-all-tagged) | supported | |
| RFC 7952 metadata | partial | parsed, validated and printed; no public accessor yet |
| XPath 1.0 + YANG functions | partial | used by must/when/leafref; no public XPath query API yet |
| Tree edits: `NewPath`, `Merge`, `Remove`, `Find` | partial | absolute paths, merge without options |
| Validation diff (`Tree.ValidateDiff`) | partial | the implicit diff of a validation only |
| anydata / anyxml instances | **unsupported** | the schema nodes compile, data instances fail with `ErrUnsupported` (U-0043) |
| RPC / action / notification data and replies, external operational tree | **unsupported** | planned |
| Full diff, merge options, apply-diff, NETCONF edit-config | **unsupported** | planned |
| yang-library build and ingest | **unsupported** | planned; the `ietf-yang-library` module itself is loaded |
| Command-line tool (yanglint) | **unsupported** | there is no CLI yet |
| YIN, LYB, schema mount (RFC 8528), tree / YANG printers | **unsupported** | out of v1 scope |

Function-level status: [docs/port-map.md](docs/port-map.md) (one row per libyang function:
ported, replaced, partial or skipped). Every limit of this port is in the *Unsupported* table of
[conformance/deviations.md](conformance/deviations.md).

## Install

```sh
go get github.com/vibe-ports/yang
```

Requires **Go 1.26** or newer (the two newest Go releases are supported). No C toolchain, no
libyang, no libpcre2: `CGO_ENABLED=0` builds and cross-compiles.

**Versioning.** The module stays at v0 until the API review before v1. Until then any release
may change the API; pin a version.

## Example

Load a module, take the schema snapshot, parse and validate data, print it and read the
diagnostics. This is [`data.Example`](data/example_test.go), run by `go test`.

```go
package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing/fstest"

	"github.com/vibe-ports/yang"
	"github.com/vibe-ports/yang/data"
)

func main() {
	models := fstest.MapFS{"example.yang": {Data: []byte(`module example {
  yang-version 1.1;
  namespace "urn:example";
  prefix ex;
  container system {
    leaf hostname { type string { pattern '[a-z][a-z0-9-]*'; } mandatory true; }
    leaf mtu { type uint16 { range "68..9000"; } default 1500; }
    list server {
      key name;
      leaf name { type string; }
      leaf port { type uint16; must ". != 0" { error-message "port 0 is reserved"; } }
    }
  }
}`)}} // or os.DirFS("models"), an embed.FS, ...

	// NoYangLibrary: without it, validating a datastore also requires the ietf-yang-library data.
	ctx, _, err := yang.NewContext(yang.Options{NoYangLibrary: true}, models)
	if err != nil {
		panic(err)
	}
	if _, err := ctx.Load("example", "", nil); err != nil {
		panic(err)
	}
	schema := ctx.Schema() // immutable snapshot, safe to share between goroutines

	in := `{"example:system": {"hostname": "edge-1", "server": [{"name": "ntp", "port": 123}]}}`
	tree, _, err := data.Parse(context.Background(), strings.NewReader(in), data.FormatJSON, schema, data.ParseOptions{})
	if err != nil {
		panic(err)
	}
	if err := tree.PrintXML(os.Stdout, data.PrintOptions{WithDefaults: data.WDAll}); err != nil {
		panic(err)
	}

	bad := `{"example:system": {"hostname": "Edge_1", "mtu": 20, "server": [{"name": "ntp", "port": 0}]}}`
	_, diags, err := data.Parse(context.Background(), strings.NewReader(bad), data.FormatJSON, schema,
		data.ParseOptions{Validate: data.ValidateOptions{MultiError: true}})
	fmt.Println("valid:", err == nil)
	for _, d := range diags {
		fmt.Println(d.Code, d.DataPath, d.Msg)
	}
}
```

Output:

```
<system xmlns="urn:example">
  <hostname>edge-1</hostname>
  <mtu>1500</mtu>
  <server>
    <name>ntp</name>
    <port>123</port>
  </server>
</system>
valid: false
LYVE_DATA /example:system/hostname Unsatisfied pattern - "Edge_1" does not match "[a-z][a-z0-9-]*".
LYVE_DATA /example:system/mtu Unsatisfied range - value "20" is out of the allowed range.
LYVE_DATA /example:system Mandatory node "hostname" instance does not exist.
LYVE_DATA /example:system/server[name='ntp']/port port 0 is reserved
```

The third diagnostic is libyang's too: the invalid `hostname` is not stored, so the mandatory
check finds no instance. Schema traversal is shown in the [`yang` package examples](example_test.go).

## How compatibility is measured

- **Oracle.** A small test-only C program ([conformance/oracle](conformance/oracle)) linked
  against libyang v5.8.6 built from a pinned commit inside the dev container. It is the only C in
  the repository and lives in a separate Go module (`conformance/`), so library users never pull it.
- **Goldens.** Each fixture in [conformance/corpus/manifest.yaml](conformance/corpus/manifest.yaml)
  (libyang's own tests, RFC examples, public models, hand-written edge cases) is a request to the
  oracle; its response is committed as a golden file. CI checks that the oracle still produces
  every golden (`make oracle-check`), and the Go packages' oracle tests must not skip.
- **Report.** `make compat-report` (= `cd conformance && go run ./cmd/report -engine yang`) runs
  every fixture through this port and compares it with the golden: module verdicts, compiled
  schema tree, data verdict and diagnostics (LY_ERR, LY_VECODE, paths, app-tag; message text and
  line are not compared here), and the printed JSON tree. CI appends the report to the job
  summary. Each fixture ends up as:
  - *agree* — the whole response matches;
  - *agree (skipped fields)* — matches once fields the port does not produce are dropped from
    both sides (the compiled-schema printout, value types, union members, metadata, opaque
    details); the report counts every skipped field;
  - *differ* — a bug;
  - *deviation* — an intentional difference, see below;
  - *unsupported* — the port refuses the input or the engine does not run that operation yet.
- **Deviations.** Where libyang contradicts the RFC, crashes or depends on the host, the port
  may differ on purpose. Each case is listed with its RFC reference and fixture in
  [conformance/deviations.md](conformance/deviations.md); an unlisted difference is a bug.

Current report (`make compat-report`):

| | agree | agree (skipped fields) | differ | deviation | unsupported |
|---|--:|--:|--:|--:|--:|
| fixtures (1 866) | 592 | 891 | 0 | 16 | 367 |

Of the 367 unsupported fixtures, 193 are operations the report engine does not run yet: 146
`xpath` (the XPath evaluator is compared with the oracle by its own tests in `internal/xpath`),
46 `sequence` (edit and revalidate a retained tree) and 1 `diff`. The other 174 are inputs the
port refuses: 82 modules with `deviation` statements, 22 `yang-data`/`structure` extension
instances, 16 bit positions above the 65 535 budget (U-0031), 26 anydata/anyxml instances, 8
operation messages, 10 requests with an external operational tree and 10 with oracle options the
engine does not map.

## Packages

| Package | |
|---|---|
| [`yang`](doc.go) | `Context`: load and compile modules from `fs.FS`; `Schema` snapshot and read-only handles |
| [`data`](data/doc.go) | data trees: JSON/XML parse and print, validation, defaults, edits |
| `internal/…` | parser, compiler, types, XPath, XSD regex — not importable |
| [`conformance`](conformance) | separate module: oracle, corpus, goldens, report |

Design notes: [docs/design](docs/design). Plan and milestones: [PLAN.md](PLAN.md).

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md). Bug reports with a minimal schema, data and the libyang
v5.8.6 result are the most useful contribution: each becomes an oracle fixture.

## License

BSD-3-Clause, see [LICENSE](LICENSE). Portions are derived from libyang, © CESNET, BSD-3-Clause;
the CESNET notice is part of [LICENSE](LICENSE). Third-party material carries its own notices:
libyang's internal YANG modules ([internal/models/NOTICE](internal/models/NOTICE)), libyang test
material in the corpus ([conformance/NOTICE](conformance/NOTICE)) and cJSON in the oracle
([conformance/oracle/cjson/LICENSE](conformance/oracle/cjson/LICENSE)).

This project is not affiliated with or endorsed by CESNET or the libyang authors.
