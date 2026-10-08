// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/plugins_types.c (ly_get_prefix and its per-format helpers,
// lyplg_type_print_simple), src/plugins_types/identityref.c (lyplg_type_print_identityref),
// src/plugins_types/instanceid.c (lyplg_type_print_instanceid, instanceid_path2str),
// src/plugins_types/union.c (lyplg_type_print_union) and src/plugins_types/leafref.c
// (lyplg_type_print_leafref) (BSD-3-Clause, © CESNET).

package types

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/vibe-ports/yang/internal/schema"
)

// PrintCtx is the prefix data a print callback gets (libyang prefix_data of lyplg_type_print_clb):
// which module's prefix is left out and, for XML, the modules whose prefix was used.
type PrintCtx struct {
	// Local is the module that needs no prefix: for JSON and canonical output the module of the
	// node being printed, for XML the module of the default namespace (the printed element's), for
	// FormatSchema the module the text is written in. nil prefixes every module.
	Local *schema.Module
	// Used collects, for FormatXML, the modules whose prefix was printed in first-use order: the
	// caller declares `xmlns:<Prefix>="<Namespace>"` for each (libyang's printer does it after the
	// value is printed). Local is never listed, except that an XML instance-identifier prefixes every
	// node, Local's own included, so Local is then listed (and kept as Local afterwards).
	Used []*schema.Module
}

// prefix ports ly_get_prefix: the prefix of m in format f, "" for none (the default namespace /
// the local module).
func (pc *PrintCtx) prefix(m *schema.Module, f Format) string {
	switch f {
	case FormatSchema: // ly_schema_get_prefix: own prefix or the import prefix
		if pc.Local == nil {
			return ""
		}
		if m == pc.Local {
			return m.Prefix
		}
		for _, im := range pc.Local.Imports {
			if im.Module == m {
				return im.Prefix
			}
		}
	case FormatXML: // ly_xml_get_prefix
		if m == pc.Local {
			return ""
		}
		if !slices.Contains(pc.Used, m) {
			pc.Used = append(pc.Used, m)
		}
		return m.Prefix
	default: // ly_json_get_prefix (also canonical)
		if m != pc.Local {
			return m.Name
		}
	}
	return ""
}

// ErrUnsupported is wrapped by errors for input this port does not handle (the root package
// re-exports it).
var ErrUnsupported = errors.New("not supported")

// Print ports the print callbacks (lyplg_type_print_clb) for every type: the value in format f.
// Most types print their canonical form in every format; identityref and instance-identifier
// qualify module names with the prefixes of f (see PrintCtx), a leafref prints as its target type,
// a union as its selected member. FormatSchemaResolved (and any unknown format) is rejected with an
// error wrapping ErrUnsupported: it needs an ordered prefix list, NSCtx is a map. pc may be nil (no
// local module, prefixes are not collected).
func Print(v Value, f Format, pc *PrintCtx) (string, error) {
	switch f {
	case FormatCanon, FormatJSON, FormatXML, FormatSchema:
	default:
		return "", fmt.Errorf("types: print in format %d: %w", f, ErrUnsupported)
	}
	if pc == nil {
		pc = &PrintCtx{}
	}
	return printValue(v, f, pc), nil
}

func printValue(v Value, f Format, pc *PrintCtx) string {
	if v.union != nil { // lyplg_type_print_union
		return printValue(v.union.member, f, pc)
	}
	if v.typ == nil {
		return v.canon
	}
	if k, ok := v.ext.(*keysValue); ok && f != FormatCanon && f != FormatJSON {
		// lyplg_type_print_xpath10 (xpath1.0.c), also for instance-identifier-keys, whose
		// lyd_value_instance_identifier_keys it reads as the identically laid out lyd_value_xpath10:
		// the expression printed with xpath10_print_subexpr_r. On an error libyang prints nothing
		// (NULL); the canonical text is kept here.
		if s, d := printXPath10(k.e, k.pc, f, pc); d == nil {
			return s
		}
		return v.canon
	}
	if n, ok := v.ext.(*nodeInstanceID); ok && f != FormatCanon && f != FormatJSON {
		// lyplg_type_print_node_instanceid (node_instanceid.c): node_instanceid_path2str in f, the
		// same as instanceid_path2str but for the special path "/"
		if n.path == nil {
			return "/"
		}
		return printPath(n.path, f, pc)
	}
	switch v.typ.Base {
	case schema.IdentityRef: // lyplg_type_print_identityref
		if f == FormatCanon {
			return v.canon
		}
		if p := pc.prefix(v.ident.Module, f); p != "" {
			return p + ":" + v.ident.Name
		}
		return v.ident.Name
	case schema.InstanceID: // lyplg_type_print_instanceid: the canonical form is JSON
		if f == FormatCanon || f == FormatJSON {
			return v.canon
		}
		return printPath(v.path, f, pc)
	}
	return v.canon // lyplg_type_print_simple
}

// printPath ports instanceid_path2str for the formats that prefix every node (XML and schema).
// The key and leaf-list values are printed in f too: libyang stores the canonical form again and
// prints that (lyplg_type_print_val), which yields the stored value itself.
func printPath(p Path, f Format, pc *PrintCtx) string {
	if f == FormatXML { // null the local module so that all the prefixes are printed
		defer func(local *schema.Module) { pc.Local = local }(pc.Local)
		pc.Local = nil
	}
	// a missing prefix prints as C's "%s" of NULL
	pfx := func(m *schema.Module) string {
		if s := pc.prefix(m, f); s != "" {
			return s
		}
		return "(null)"
	}
	var b strings.Builder
	for _, s := range p {
		fmt.Fprintf(&b, "/%s:%s", pfx(s.Node.Module), s.Node.Name)
		for _, pr := range s.Preds {
			switch pr.Kind {
			case PredPosition:
				fmt.Fprintf(&b, "[%d]", pr.Position)
			case PredKey: // the value first: it may add to pc.Used before the key's prefix
				val := quoteXP(printValue(pr.Value, f, pc))
				fmt.Fprintf(&b, "[%s:%s=%s]", pfx(pr.Key.Module), pr.Key.Name, val)
			case PredLeafList:
				fmt.Fprintf(&b, "[.=%s]", quoteXP(printValue(pr.Value, f, pc)))
			}
		}
	}
	return b.String()
}
