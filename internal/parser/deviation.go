// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/parser_yang.c (parse_deviation, parse_deviate) and the
// lysp_deviation / lysp_deviate* structures of src/tree_schema.h (BSD-3-Clause, © CESNET).

package parser

// Deviation is a deviation statement (struct lysp_deviation). Its values (defaults, uniques,
// must expressions, types, extension instances) are written in the (sub)module whose
// Module.Deviations holds it: that is the prefix context libyang keeps in lysp_qname.mod and
// lysp_restr.arg.mod.
type Deviation struct {
	Nodeid                 string // target absolute schema node-id
	Description, Reference string
	Deviates               []*Deviate // in statement order
	Exts                   []*Stmt    // the exts array: instances of the deviation and its description/reference
	Stmt                   *Stmt
}

// Deviate is a deviate statement (struct lysp_deviate, lysp_deviate_add, _del, _rpl). Mod is
// its argument: "not-supported", "add", "delete" or "replace". The parser (deviateAllows) lets
// each kind hold only the properties libyang's structure for it has.
type Deviate struct {
	Mod                      string
	Units                    *string
	Musts                    []*Restr // add, delete
	Uniques, Defaults        []string // uniques: add, delete; replace has at most one default
	Config, Mandatory        *bool    // add, replace
	MinElements, MaxElements *uint32  // add, replace; MaxElements 0 = unbounded
	Type                     *Type    // replace
	Exts                     []*Stmt  // the exts array: instances of the deviate and of its substatements other than must and type
	Stmt                     *Stmt
}

// deviation is parse_deviation over the (already checked) statement s.
func (b *builder) deviation(s *Stmt) *Deviation {
	d := &Deviation{Nodeid: s.Arg, Exts: OwnedExts(s), Stmt: s}
	for _, c := range s.Subs {
		if c.ExtPrefix != "" {
			continue
		}
		switch c.Keyword {
		case "description":
			d.Description = c.Arg
		case "reference":
			d.Reference = c.Arg
		case "deviate":
			d.Deviates = append(d.Deviates, b.deviate(c))
		}
	}
	return d
}

// deviate is parse_deviate: the substatements are those of a data node, so the node builder
// reads them.
func (b *builder) deviate(s *Stmt) *Deviate {
	n := b.child(s)
	return &Deviate{Mod: s.Arg, Units: n.Units, Musts: n.Musts, Uniques: n.Uniques, Defaults: n.Defaults,
		Config: n.Config, Mandatory: n.Mandatory, MinElements: n.MinElements, MaxElements: n.MaxElements,
		Type: n.Type, Exts: OwnedExts(s), Stmt: s}
}
