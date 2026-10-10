# compile/ — schema-compilation fixtures (design 06 s6)

Modules in `schemas/` are hand-written (BSD-3-Clause) or adapted from libyang v5.8.6 utests
(BSD-3-Clause, © CESNET; see `conformance/NOTICE`; the file header names the test), except:

| file | origin | license |
|---|---|---|
| ietf-netconf-acm@2018-02-14.yang | reduced from RFC 8341 (only the `default-deny-write` / `default-deny-all` extension definitions; libyang's full copy is `corpus/ietf/`) | IETF Trust, Simplified BSD (BSD-2-Clause) |
| ietf-restconf@2017-01-26.yang | reduced from RFC 8040 (only the `yang-data` extension definition) | IETF Trust, Simplified BSD (BSD-2-Clause) |

The reductions keep module name, namespace, prefix and revision, which is what libyang's extension
plugins match on. Goldens are libyang v5.8.6 oracle output (linux/amd64).
