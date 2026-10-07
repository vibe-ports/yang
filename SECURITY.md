# Security policy

## Reporting a vulnerability

Report privately through GitHub's private vulnerability reporting:
[Security → Report a vulnerability](https://github.com/vibe-ports/yang/security/advisories/new).
Do not open a public issue, pull request or discussion for a vulnerability.

Include the smallest YANG modules and data that reproduce it, the commands you ran, what happened
(crash, hang, memory or CPU use, wrong verdict) and the module version or commit. Remove private
data first: your own models, host names, addresses and credentials.

The maintainer answers in the advisory, fixes the problem in a private fork when needed and
publishes the advisory with the fix. There is no bug bounty.

## Supported versions

The project is at v0.x. Only the latest `main` (and the newest v0.x release, once there is one) gets
fixes; there are no backports to older versions.

## Scope

In scope: anything in the root module `github.com/vibe-ports/yang` (packages `yang`, `data` and the
`internal/` packages they use) that an attacker can reach with untrusted input, in particular:
- parsing untrusted YANG modules and untrusted JSON or XML data: panics, crashes, unbounded memory
  or CPU, hangs, or input that escapes the module search directories the caller gave;
- resource limits: inputs that get around a budget (nesting depth, expansion size, regex size,
  XPath steps, value sizes) or cancellation;
- validation that accepts data libyang v5.8.6 rejects (or the reverse) in a way a caller could
  rely on for a security decision, unless recorded in `conformance/deviations.md`.

Out of scope: the test-only `conformance/` module and its C oracle helper, the dev container and
the scripts in `scripts/` (report problems there as ordinary issues unless they expose a secret),
and vulnerabilities in libyang itself (report those to CESNET's libyang project).
