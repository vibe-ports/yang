# public-ietf/ — published IETF and IANA YANG modules (issue #39)

Byte-identical copies from https://github.com/YangModels/yang at commit
`b5465a86cd2457352dcd18a42043e40f0bab65ab`: IETF modules from `standard/ietf/RFC/` (the revision
published in the RFC) and IANA-maintained modules from `standard/iana/` (latest revision there).
They are Code Components of IETF documents, under the IETF Trust Legal Provisions: the Simplified
BSD License (BSD-2-Clause), or the Revised BSD License (BSD-3-Clause) for modules published under
the 2021+ provisions. The license text is inside each module and must stay there.

Each implementable module is an op `schema` fixture with all features enabled (`-all-features`)
and, when it defines features, with none (`-no-features`); its imports are loaded from `schemas/`.
`ietf-yang-types`, `ietf-inet-types`, `ietf-yang-library`, `ietf-datastores` and
`ietf-yang-schema-mount` are not copied: libyang (and this port) provide them internally.

| file | RFC | license |
|---|---|---|
| iana-bfd-types@2026-07-02.yang | 9314 (IANA-maintained) | BSD-2-Clause |
| iana-crypt-hash@2014-08-06.yang | 7317 (IANA-maintained) | BSD-2-Clause |
| iana-hardware@2018-03-13.yang | 8348 (IANA-maintained) | BSD-2-Clause |
| iana-if-type@2026-03-17.yang | 7224 (IANA-maintained) | BSD-2-Clause |
| iana-routing-types@2025-09-03.yang | 8294 (IANA-maintained) | BSD-2-Clause |
| ietf-access-control-list@2019-03-04.yang | 8519 | BSD-2-Clause |
| ietf-alarms@2022-06-06.yang | 8632 | BSD-2-Clause |
| ietf-bfd-types@2022-09-22.yang | 9314 | BSD-3-Clause |
| ietf-ethertypes@2019-03-04.yang | 8519 | BSD-2-Clause |
| ietf-hardware@2018-03-13.yang | 8348 | BSD-2-Clause |
| ietf-interfaces@2018-02-20.yang | 8343 | BSD-2-Clause |
| ietf-ip@2018-02-22.yang | 8344 | BSD-2-Clause |
| ietf-ipv4-unicast-routing@2018-03-13.yang | 8349 | BSD-2-Clause |
| ietf-ipv6-router-advertisements@2018-03-13.yang (submodule of ietf-ipv6-unicast-routing) | 8349 | BSD-2-Clause |
| ietf-ipv6-unicast-routing@2018-03-13.yang | 8349 | BSD-2-Clause |
| ietf-key-chain@2017-06-15.yang | 8177 | BSD-2-Clause |
| ietf-netconf-acm@2018-02-14.yang | 8341 | BSD-2-Clause |
| ietf-netconf-monitoring@2010-10-04.yang | 6022 | BSD-2-Clause |
| ietf-netconf-notifications@2012-02-06.yang | 6470 | BSD-2-Clause |
| ietf-netconf-with-defaults@2011-06-01.yang | 6243 | BSD-2-Clause |
| ietf-netconf@2011-06-01.yang | 6241 | BSD-2-Clause |
| ietf-network-instance@2019-01-21.yang | 8529 | BSD-2-Clause |
| ietf-network-topology@2018-02-26.yang | 8345 | BSD-2-Clause |
| ietf-network@2018-02-26.yang | 8345 | BSD-2-Clause |
| ietf-ntp@2022-07-05.yang | 9249 | BSD-3-Clause |
| ietf-ospf@2022-10-19.yang | 9129 | BSD-3-Clause |
| ietf-packet-fields@2019-03-04.yang | 8519 | BSD-2-Clause |
| ietf-restconf-monitoring@2017-01-26.yang | 8040 | BSD-2-Clause |
| ietf-restconf@2017-01-26.yang | 8040 | BSD-2-Clause |
| ietf-routing-types@2017-12-04.yang | 8294 | BSD-2-Clause |
| ietf-routing@2018-03-13.yang | 8349 | BSD-2-Clause |
| ietf-subscribed-notifications@2019-09-09.yang | 8639 | BSD-2-Clause |
| ietf-system@2014-08-06.yang | 7317 | BSD-2-Clause |
| ietf-vrrp@2018-03-13.yang | 8347 | BSD-2-Clause |
| ietf-yang-patch@2017-02-22.yang | 8072 | BSD-2-Clause |
| ietf-yang-push@2019-09-09.yang | 8641 | BSD-2-Clause |
