# Known deviations from libyang v5.8.6

Every intentional difference between this port and libyang, and every libyang behaviour we consider
questionable. Format: id · area · libyang behaviour · ours · RFC reference · fixture.

| ID | Area | libyang | Ours | RFC | Fixture |
|---|---|---|---|---|---|
| D-0001 (candidate) | `when` + defaults | `when` reading a **top-level** default leaf rejects, same check one level down accepts (found in ADR 0002, cases 24/26) | undecided — mirror libyang until analysed | RFC 7950 §7.6.1, §7.21.5 | spike/g2-cambium/cases/24, 26 |
