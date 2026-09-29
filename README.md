# yang

A pure-Go (no cgo) YANG 1.1 library: schema parsing and compilation, a generic
data tree, RFC 7950 validation, RFC 7951 JSON and XML encoding, diff.

This is an **AI-assisted port of [libyang](https://github.com/CESNET/libyang)**
(v5.8.6) to idiomatic Go. Most code is translated from libyang's C sources by
AI coding agents (Anthropic Claude, OpenAI Codex), working to a written plan and
checked against libyang itself: the same schemas and data must yield the same
validation result.

Not affiliated with or endorsed by CESNET or the libyang authors.
Portions are derived from libyang, © CESNET, BSD-3-Clause — see LICENSE.

Status: planning. See [PLAN.md](PLAN.md).
