// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/schema_compile_amend.c (lys_apply_deviate_delete,
// lys_apply_deviate_ext_inst_find) (BSD-3-Clause, © CESNET).

package compile

import (
	"slices"

	"github.com/vibe-ports/yang/internal/ly"
	"github.com/vibe-ports/yang/internal/parser"
)

// deviateDelete is lys_apply_deviate_delete: d, written in w.pm, removes properties of the
// parsed copy t.n. Values are matched by their text.
func (w *nodeCtx) deviateDelete(d *parser.Deviate, t *devTarget) error {
	n := t.n
	noneMatch := func(prop, val string) error {
		return w.errf(ly.Reference,
			"Invalid deviation deleting \"%s\" property \"%s\" which does not match any of the target's property values.", prop, val)
	}
	// presence is DEV_CHECK_PRESENCE_VALUE
	presence := func(prop string, cur *string, val string) error {
		switch {
		case cur == nil:
			return w.errf(ly.Reference, "Invalid deviation deleting \"%s\" property \"%s\" which is not present.", prop, val)
		case *cur != val:
			return w.errf(ly.Reference,
				"Invalid deviation deleting \"%s\" property \"%s\" which does not match the target's property value \"%s\".",
				prop, val, *cur)
		}
		return nil
	}

	// [units-stmt]
	if d.Units != nil {
		if n.Kind != "leaf" && n.Kind != "leaf-list" {
			return w.wrongNodetype(t, "delete", "units")
		}
		if err := presence("units", n.Units, *d.Units); err != nil {
			return err
		}
		n.Units = nil
	}

	// *must-stmt (DEV_DEL_ARRAY)
	if len(d.Musts) > 0 {
		if !hasMusts(n.Kind) {
			return w.wrongNodetype(t, "delete", "must")
		}
		for _, m := range d.Musts {
			v := slices.IndexFunc(n.Musts, func(o *parser.Restr) bool { return o.Arg == m.Arg })
			if v < 0 {
				return noneMatch("must", m.Arg)
			}
			n.Musts = slices.Delete(slices.Clone(n.Musts), v, v+1)
		}
		if len(n.Musts) == 0 {
			n.Musts = nil
		}
	}

	// *unique-stmt
	if len(d.Uniques) > 0 {
		if n.Kind != "list" {
			return w.wrongNodetype(t, "delete", "unique")
		}
		o := w.uniqs(t)
		for _, u := range d.Uniques {
			v := slices.Index(n.Uniques, u)
			if v < 0 {
				return noneMatch("unique", u)
			}
			n.Uniques = slices.Delete(slices.Clone(n.Uniques), v, v+1)
			o = slices.Delete(o, v, v+1)
		}
		w.dev.uniqs[n] = o
	}

	// *default-stmt
	if len(d.Defaults) > 0 {
		switch n.Kind {
		case "leaf", "choice":
			if err := w.cardinality(t, len(d.Defaults), "default"); err != nil {
				return err
			}
			var cur *string
			if len(n.Defaults) > 0 {
				cur = &n.Defaults[0]
			}
			if err := presence("default", cur, d.Defaults[0]); err != nil {
				return err
			}
			n.Defaults = nil
		case "leaf-list":
			o := w.dflts(t)
			for _, df := range d.Defaults {
				v := slices.Index(n.Defaults, df)
				if v < 0 {
					return noneMatch("default", df)
				}
				n.Defaults = slices.Delete(slices.Clone(n.Defaults), v, v+1)
				o = slices.Delete(o, v, v+1)
			}
			w.dev.dflts[n] = o
		default:
			return w.wrongNodetype(t, "delete", "default")
		}
	}

	// *ext-inst
	arr, _ := w.c.ownedExts(d.Stmt)
	if len(arr) == 0 {
		return nil
	}
	l := slices.Clone(w.exts(t))
	for _, e := range arr {
		v := w.extInstFind(e, w.pm, l)
		if v < 0 {
			arg := ""
			if e.HasArg {
				arg = " " + e.Arg
			}
			return w.errf(ly.Reference, "Invalid deviation deleting \"ext-inst\" property \"%s:%s%s\" "+
				"which does not match any of the target's property values.", e.ExtPrefix, e.Keyword, arg)
		}
		l = slices.Delete(l, v, v+1)
	}
	w.dev.exts[n] = l
	return nil
}

// extInstFind is lys_apply_deviate_ext_inst_find: the index in l of the first instance of the
// node itself with the definition and argument of e (written in pm), -1 if none.
func (w *nodeCtx) extInstFind(e *parser.Stmt, pm *pmod, l []extIn) int {
	def := w.c.prefixModule(pm, pm.main, e.ExtPrefix)
	return slices.IndexFunc(l, func(x extIn) bool {
		return x.node && x.e.Keyword == e.Keyword && w.c.prefixModule(x.pm, x.pm.main, x.e.ExtPrefix) == def &&
			x.e.HasArg == e.HasArg && x.e.Arg == e.Arg
	})
}
