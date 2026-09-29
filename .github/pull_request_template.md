## What
<!-- libyang file/functions ported or feature added -->

## Checklist
- [ ] `./dev make ci` green (includes oracle-check, nocgo, 386, govulncheck)
- [ ] Ported files carry SPDX + "Ported from libyang v5.8.6 src/…" header
- [ ] `docs/port-map.md` updated
- [ ] Oracle fixtures added for new behaviour (valid + invalid); goldens regenerated, not hand-edited
- [ ] Differences from libyang are fixed or listed in `conformance/deviations.md` with RFC reason
- [ ] No new dependency / exported symbol without a note below
- [ ] Public sources only; no secrets; no employer material

## Notes
