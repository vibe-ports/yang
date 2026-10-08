// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/schema_compile.c (lys_compile_unres_mod) (BSD-3-Clause, © CESNET).

package compile

import "github.com/vibe-ports/yang/internal/ly"

// pendingAug is an augment of another module targeting the module being compiled, not applied
// by the node walk (struct lysc_augment of ctx->augs, design 06 C6): its target node-id, the
// (sub)module it is written in and, for an augment in an extension instance, the instance name.
type pendingAug struct {
	nodeid string
	pm     *pmod
	ext    string
}

// unresMod is P5, lys_compile_unres_mod: every augment left unapplied is logged, all of them,
// then the compile fails with LY_ENOTFOUND. The deviations follow (unresDeviations).
func (w *nodeCtx) unresMod(augs []pendingAug) error {
	orig := w.path.cur
	for _, a := range augs {
		w.path.cur = a.pm.mod
		if a.ext != "" {
			w.path.update(nil, "{ext-inst}")
			w.path.update(nil, a.ext)
		}
		w.path.update(nil, "{augment}")
		w.path.update(nil, a.nodeid)
		extInst := ""
		if a.ext != "" {
			extInst = " ext-inst"
		}
		_ = w.errf(ly.Reference, "Augment%s target node \"%s\" from module \"%s\" was not found.",
			extInst, a.nodeid, a.pm.Parsed.Name)
		w.path.cur = orig
		w.path.pop()
		w.path.pop()
		if a.ext != "" {
			w.path.pop()
			w.path.pop()
		}
	}
	if len(augs) > 0 {
		return eNotFound
	}
	return nil
}
