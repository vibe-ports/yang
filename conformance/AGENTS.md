# conformance/ — oracle rules

- `oracle/lyoracle` (C, links libyang) is the reference. It is built and run **only** in the dev
  container: `./dev make oracle-check`, `./dev make oracle-golden`.
- A fixture = entry in `corpus/manifest.yaml` (format: `manifest.schema.md`) + inputs under
  `corpus/<set>/` + golden output under `corpus/<set>/golden/`. Every fixture carries its source
  URL/commit, license and RFC section tags.
- Goldens are generated, never hand-edited. If a golden changes, the PR explains why.
- Disagreement between our code and a golden is either a bug (fix the code) or a documented entry in
  `deviations.md` with the RFC text that justifies it.
- Only public inputs; each imported set needs a license that allows redistribution.
