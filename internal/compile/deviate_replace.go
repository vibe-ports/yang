// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/schema_compile_amend.c (lys_apply_deviate_replace)
// (BSD-3-Clause, © CESNET).

package compile

import (
	"slices"

	"github.com/vibe-ports/yang/internal/ly"
	"github.com/vibe-ports/yang/internal/parser"
)

// deviateReplace is lys_apply_deviate_replace: d, written in w.pm, replaces properties of the
// parsed copy t.n. A replaced type is compiled with the prefixes of w.pm (lysp_type_dup sets its
// pmod) in the scope of the target node.
func (w *nodeCtx) deviateReplace(d *parser.Deviate, t *devTarget) error {
	n := t.n
	// present is DEV_CHECK_PRESENCE
	present := func(ok bool, prop, val string) error {
		if !ok {
			return w.errf(ly.Reference, "Invalid deviation replacing \"%s\" property \"%s\" which is not present.", prop, val)
		}
		return nil
	}

	// [type-stmt]
	if d.Type != nil {
		if n.Kind != "leaf" && n.Kind != "leaf-list" {
			return w.wrongNodetype(t, "replace", "type")
		}
		n.Type = d.Type
		w.from[d.Type] = w.pm
	}

	// [units-stmt]
	if d.Units != nil {
		if n.Kind != "leaf" && n.Kind != "leaf-list" {
			return w.wrongNodetype(t, "replace", "units")
		}
		if err := present(n.Units != nil, "units", *d.Units); err != nil {
			return err
		}
		n.Units = d.Units
	}

	// [default-stmt]
	if len(d.Defaults) > 0 {
		switch n.Kind {
		case "leaf", "choice":
			if err := present(len(n.Defaults) > 0, "default", d.Defaults[0]); err != nil {
				return err
			}
			n.Defaults = slices.Clone(d.Defaults)
			w.from[dfltKey{n}] = w.pm
		default:
			return w.wrongNodetype(t, "replace", "default")
		}
	}

	// [config-stmt]
	if d.Config != nil {
		switch n.Kind {
		case "container", "leaf", "leaf-list", "list", "choice", "anydata", "anyxml":
		default:
			return w.wrongNodetype(t, "replace", "config")
		}
		n.Config = d.Config
	}

	// [mandatory-stmt]
	if d.Mandatory != nil {
		switch n.Kind {
		case "leaf", "choice", "anydata", "anyxml":
		default:
			return w.wrongNodetype(t, "replace", "mandatory")
		}
		n.Mandatory = d.Mandatory
	}

	// [min-elements-stmt]
	if d.MinElements != nil {
		if n.Kind != "leaf-list" && n.Kind != "list" {
			return w.wrongNodetype(t, "replace", "min-elements")
		}
		n.MinElements = d.MinElements
	}

	// [max-elements-stmt]
	if d.MaxElements != nil {
		if n.Kind != "leaf-list" && n.Kind != "list" {
			return w.wrongNodetype(t, "replace", "max-elements")
		}
		n.MaxElements = d.MaxElements
	}

	// *ext-inst: each replaces the first instance of the node with its definition and argument
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
			return w.errf(ly.Reference, "Invalid deviation replacing \"ext-inst\" property \"%s:%s%s\" which is not present.",
				e.ExtPrefix, e.Keyword, arg)
		}
		l[v] = extIn{e, d.Stmt, w.pm, l[v].node}
	}
	w.dev.exts[n] = l
	return nil
}
