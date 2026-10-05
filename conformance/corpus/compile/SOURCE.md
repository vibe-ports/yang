# compile/ — schema-compilation fixtures (design 06 s6)

All modules in `schemas/` are hand-written for this repo (BSD-3-Clause), except:

| file | origin | license |
|---|---|---|
| ietf-netconf-acm@2018-02-14.yang | reduced from RFC 8341 (only the `default-deny-write` / `default-deny-all` extension definitions; libyang ships no copy) | IETF Trust, Simplified BSD (BSD-2-Clause) |
| ietf-restconf@2017-01-26.yang | reduced from RFC 8040 (only the `yang-data` extension definition) | IETF Trust, Simplified BSD (BSD-2-Clause) |

The reductions keep module name, namespace, prefix and revision, which is what libyang's extension
plugins match on. Goldens are libyang v5.8.6 oracle output (linux/amd64).
