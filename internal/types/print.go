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
	"strconv"
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

// printBudget charges the work and produced bytes of a dynamic type conversion in the same
// 64-byte units as the data printer. Each unit is charged before the conversion can grow a
// builder. Keeping the partial unit here is important: xpath1.0 commonly appends many short
// tokens, none of which may escape the cumulative byte budget.
type printBudget struct {
	step func() error
	left int
}

func (b *printBudget) charge(n int) error {
	for b != nil && b.step != nil && n > 0 {
		if b.left == 0 {
			if err := b.step(); err != nil {
				return err
			}
			b.left = 64
		}
		used := min(n, b.left)
		b.left -= used
		n -= used
	}
	return nil
}

// printBuilder charges every append before strings.Builder may allocate for it.
type printBuilder struct {
	b strings.Builder
	p *printBudget
}

func (b *printBuilder) writeString(s string) error {
	if err := b.p.charge(len(s)); err != nil {
		return err
	}
	_, _ = b.b.WriteString(s)
	return nil
}

func (b *printBuilder) writeByte(c byte) error {
	if err := b.p.charge(1); err != nil {
		return err
	}
	_ = b.b.WriteByte(c)
	return nil
}

func (b *printBuilder) writeBytes(p []byte) error {
	if err := b.p.charge(len(p)); err != nil {
		return err
	}
	_, _ = b.b.Write(p)
	return nil
}

func (b *printBuilder) String() string { return b.b.String() }

// ErrUnsupported is wrapped by errors for input this port does not handle (the root package
// re-exports it).
var ErrUnsupported = errors.New("not supported")

// Print ports the print callbacks (lyplg_type_print_clb) for every type: the value in format f.
// Most types print their canonical form in every format; identityref and instance-identifier
// qualify module names with the prefixes of f (see PrintCtx), a leafref prints as its target type,
// a union as its selected member. FormatSchemaResolved (and any unknown format) is rejected with an
// error wrapping ErrUnsupported: it needs an ordered prefix list, NSCtx is a map. pc may be nil (no
// local module, prefixes are not collected). The optional byteStep is used by XPath data casts;
// each call reserves the next 64 bytes of conversion work/output before a dynamic allocation.
func Print(v Value, f Format, pc *PrintCtx, byteStep ...func() error) (string, error) {
	switch f {
	case FormatCanon, FormatJSON, FormatXML, FormatSchema:
	default:
		return "", fmt.Errorf("types: print in format %d: %w", f, ErrUnsupported)
	}
	if pc == nil {
		pc = &PrintCtx{}
	}
	var step func() error
	if len(byteStep) != 0 {
		step = byteStep[0]
	}
	return printValue(v, f, pc, &printBudget{step: step})
}

func printValue(v Value, f Format, pc *PrintCtx, budget *printBudget) (string, error) {
	if v.union != nil { // lyplg_type_print_union
		return printValue(v.union.member, f, pc, budget)
	}
	if v.typ == nil {
		return printCanonical(v.canon, budget)
	}
	if k, ok := v.ext.(*keysValue); ok && f != FormatCanon && f != FormatJSON {
		// lyplg_type_print_xpath10 (xpath1.0.c), also for instance-identifier-keys, whose
		// lyd_value_instance_identifier_keys it reads as the identically laid out lyd_value_xpath10:
		// the expression printed with xpath10_print_subexpr_r. A failed conversion
		// returns NULL in libyang; never replace it with unconverted canonical text.
		return printXPath10(k.e, k.pc, f, pc, budget)
	}
	if n, ok := v.ext.(*nodeInstanceID); ok && f != FormatCanon && f != FormatJSON {
		// lyplg_type_print_node_instanceid (node_instanceid.c): node_instanceid_path2str in f, the
		// same as instanceid_path2str but for the special path "/"
		if n.path == nil {
			if err := budget.charge(1); err != nil {
				return "", err
			}
			return "/", nil
		}
		return printPath(n.path, f, pc, budget)
	}
	switch v.typ.Base {
	case schema.IdentityRef: // lyplg_type_print_identityref
		if f == FormatCanon {
			return printCanonical(v.canon, budget)
		}
		// Resolving and converting the stored identity is work proportional to its canonical
		// form; a qualified result below is a separate dynamic allocation.
		if err := budget.charge(max(1, len(v.canon))); err != nil {
			return "", err
		}
		if p := pc.prefix(v.ident.Module, f); p != "" {
			if err := budget.charge(len(p) + 1 + len(v.ident.Name)); err != nil {
				return "", err
			}
			return p + ":" + v.ident.Name, nil
		}
		return v.ident.Name, nil
	case schema.InstanceID: // lyplg_type_print_instanceid: the canonical form is JSON
		if f == FormatCanon || f == FormatJSON {
			return printCanonical(v.canon, budget)
		}
		return printPath(v.path, f, pc, budget)
	}
	return printCanonical(v.canon, budget) // lyplg_type_print_simple
}

func printCanonical(canon string, budget *printBudget) (string, error) {
	if err := budget.charge(max(1, len(canon))); err != nil {
		return "", err
	}
	return canon, nil
}

// printPath ports instanceid_path2str for the formats that prefix every node (XML and schema).
// The key and leaf-list values are printed in f too: libyang stores the canonical form again and
// prints that (lyplg_type_print_val), which yields the stored value itself.
func printPath(p Path, f Format, pc *PrintCtx, budget *printBudget) (string, error) {
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
	b := printBuilder{p: budget}
	for _, s := range p {
		if err := b.writeByte('/'); err != nil {
			return "", err
		}
		if err := b.writeString(pfx(s.Node.Module)); err != nil {
			return "", err
		}
		if err := b.writeByte(':'); err != nil {
			return "", err
		}
		if err := b.writeString(s.Node.Name); err != nil {
			return "", err
		}
		for _, pr := range s.Preds {
			switch pr.Kind {
			case PredPosition:
				var raw [20]byte
				n := strconv.AppendUint(raw[:0], pr.Position, 10)
				if err := b.writeByte('['); err != nil {
					return "", err
				}
				if err := b.writeBytes(n); err != nil {
					return "", err
				}
				if err := b.writeByte(']'); err != nil {
					return "", err
				}
			case PredKey: // the value first: it may add to pc.Used before the key's prefix
				value, err := printValue(pr.Value, f, pc, budget)
				if err != nil {
					return "", err
				}
				for _, text := range []string{"[", pfx(pr.Key.Module), ":", pr.Key.Name, "="} {
					if err := b.writeString(text); err != nil {
						return "", err
					}
				}
				quote := byte('\'')
				if strings.IndexByte(value, quote) >= 0 {
					quote = '"'
				}
				if err := b.writeByte(quote); err != nil {
					return "", err
				}
				if err := b.writeString(value); err != nil {
					return "", err
				}
				if err := b.writeByte(quote); err != nil {
					return "", err
				}
				if err := b.writeByte(']'); err != nil {
					return "", err
				}
			case PredLeafList:
				value, err := printValue(pr.Value, f, pc, budget)
				if err != nil {
					return "", err
				}
				if err := b.writeString("[.="); err != nil {
					return "", err
				}
				quote := byte('\'')
				if strings.IndexByte(value, quote) >= 0 {
					quote = '"'
				}
				if err := b.writeByte(quote); err != nil {
					return "", err
				}
				if err := b.writeString(value); err != nil {
					return "", err
				}
				if err := b.writeByte(quote); err != nil {
					return "", err
				}
				if err := b.writeByte(']'); err != nil {
					return "", err
				}
			}
		}
	}
	return b.String(), nil
}
