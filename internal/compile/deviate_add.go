// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/schema_compile_amend.c (lys_apply_deviate_add)
// (BSD-3-Clause, © CESNET).

package compile

import (
	"slices"
	"strconv"

	"github.com/vibe-ports/yang/internal/ly"
	"github.com/vibe-ports/yang/internal/parser"
)

// deviateAdd is lys_apply_deviate_add: d, written in w.pm, adds properties to the parsed copy
// t.n. The node compile that follows makes the checks of the changed node (mandatory with a
// default, min > max, config under state, the default against the type).
func (w *nodeCtx) deviateAdd(d *parser.Deviate, t *devTarget) error {
	n := t.n
	exists := func(prop, val string) error {
		return w.errf(ly.Reference, "Invalid deviation adding \"%s\" property which already exists (with value \"%s\").", prop, val)
	}

	// [units-stmt]
	if d.Units != nil {
		if n.Kind != "leaf" && n.Kind != "leaf-list" {
			return w.wrongNodetype(t, "add", "units")
		}
		if n.Units != nil {
			return exists("units", *n.Units)
		}
		n.Units = d.Units
	}

	// *must-stmt
	if len(d.Musts) > 0 {
		if !hasMusts(n.Kind) {
			return w.wrongNodetype(t, "add", "must")
		}
		n.Musts = append(slices.Clip(n.Musts), d.Musts...)
		for _, m := range d.Musts {
			w.from[m] = w.pm
		}
	}

	// *unique-stmt
	if len(d.Uniques) > 0 {
		if n.Kind != "list" {
			return w.wrongNodetype(t, "add", "unique")
		}
		o := w.uniqs(t)
		n.Uniques = append(slices.Clip(n.Uniques), d.Uniques...)
		for range d.Uniques {
			o = append(o, w.pm)
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
			if len(n.Defaults) > 0 {
				return exists("default", n.Defaults[0])
			}
			n.Defaults = slices.Clone(d.Defaults)
			w.from[dfltKey{n}] = w.pm
		case "leaf-list":
			o := w.dflts(t)
			n.Defaults = append(slices.Clip(n.Defaults), d.Defaults...)
			for range d.Defaults {
				o = append(o, w.pm)
			}
			w.dev.dflts[n] = o
		default:
			return w.wrongNodetype(t, "add", "default")
		}
	}

	// [config-stmt]
	if d.Config != nil {
		switch n.Kind {
		case "container", "leaf", "leaf-list", "list", "choice", "anydata", "anyxml":
		default:
			return w.wrongNodetype(t, "add", "config")
		}
		if n.Config != nil {
			return exists("config", "config "+boolStr(*n.Config))
		}
		n.Config = d.Config
	}

	// [mandatory-stmt]
	if d.Mandatory != nil {
		switch n.Kind {
		case "leaf", "choice", "anydata", "anyxml":
		default:
			return w.wrongNodetype(t, "add", "mandatory")
		}
		if n.Mandatory != nil {
			return exists("mandatory", "mandatory "+boolStr(*n.Mandatory))
		}
		n.Mandatory = d.Mandatory
	}

	// [min-elements-stmt]
	if d.MinElements != nil {
		if n.Kind != "leaf-list" && n.Kind != "list" {
			return w.wrongNodetype(t, "add", "min-elements")
		}
		if n.MinElements != nil {
			return exists("min-elements", fmtUint(*n.MinElements))
		}
		n.MinElements = d.MinElements
	}

	// [max-elements-stmt]
	if d.MaxElements != nil {
		if n.Kind != "leaf-list" && n.Kind != "list" {
			return w.wrongNodetype(t, "add", "max-elements")
		}
		if n.MaxElements != nil {
			v := "unbounded"
			if *n.MaxElements != 0 {
				v = fmtUint(*n.MaxElements)
			}
			return exists("max-elements", v)
		}
		n.MaxElements = d.MaxElements
	}

	// *ext-inst
	if arr, _ := w.c.ownedExts(d.Stmt); len(arr) > 0 {
		l := w.exts(t)
		for _, e := range arr {
			l = append(l, extIn{e, d.Stmt, w.pm, true})
		}
		w.dev.exts[n] = l
	}
	return nil
}

// hasMusts reports whether a node of kind has musts (lysp_node_musts_p is not NULL).
func hasMusts(kind string) bool {
	switch kind {
	case "container", "leaf", "leaf-list", "list", "anyxml", "anydata", "notification", "input", "output":
		return true
	}
	return false
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

func fmtUint(v uint32) string { return strconv.FormatUint(uint64(v), 10) }
