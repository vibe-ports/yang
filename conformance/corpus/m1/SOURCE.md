# m1/ — slice corpus (design 05)

Schemas in `schemas/`:

| file | origin | license |
|---|---|---|
| ietf-interfaces@2018-02-20.yang (RFC 8343) | YangModels/yang `standard/ietf/RFC/` | IETF Trust, Simplified BSD |
| ietf-ip@2018-02-22.yang (RFC 8344) | YangModels/yang `standard/ietf/RFC/` | IETF Trust, Simplified BSD |
| iana-if-type@2026-03-17.yang (latest revision at the pinned commit) | YangModels/yang `standard/iana/` | IETF Trust, Simplified BSD |
| m1-ext.yang | hand-written (this repo) | BSD-3-Clause |

Upstream: https://github.com/YangModels/yang at commit `0e28ed86b0a9070b7f15fd06ba4fa501723fac84`
(same commit as `../ietf/SOURCE.md`). Each upstream module carries "Redistribution and use in
source and binary forms, with or without modification, is permitted pursuant to, and subject to the
license terms contained in, the Simplified BSD License set forth in Section 4.c of the IETF Trust's
Legal Provisions Relating to IETF Documents" in its description (checked in all three files), so
they may be redistributed unmodified.

`ietf-yang-types` and `ietf-inet-types` are NOT copied: the oracle resolves them from libyang's
internal module dir (libyang v5.8.6 bundles revision 2025-12-22 of both, verified in the dev image
at `/opt/libyang/share/yang/modules/libyang`), so the revisions the fixtures use are exactly the
ones libyang ships.

`data/*` and `golden/*` are hand-written inputs / oracle output (BSD-3-Clause, this repo).
