// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/parser_yang.c (the lysp structures it fills)
// (BSD-3-Clause, © CESNET).

package parser

import (
	"strconv"

	"github.com/vibe-ports/yang/internal/ly"
)

// Module is a parsed module or submodule (libyang struct lysp_module). Its body
// statements live in the embedded Node: Children (data definitions), Typedefs,
// Groupings, Actions (rpc), Notifications, Augments.
type Module struct {
	Node
	Submodule             bool
	Version               string // "1" or "1.1"; "" means 1
	Namespace, Prefix     string // Prefix of a submodule is its belongs-to prefix
	BelongsTo             string
	Imports               []*Import
	Includes              []*Include
	Organization, Contact string
	Revisions             []*Revision
	Extensions            []*Node // Argument holds the argument name
	Features, Identities  []*Node
	Deviations            []*Deviation
}

// Import is an import statement.
type Import struct {
	Name, Prefix, RevisionDate, Description, Reference string
	Stmt                                               *Stmt
}

// Include is an include statement.
type Include struct {
	Name, RevisionDate, Description, Reference string
	Stmt                                       *Stmt
}

// Revision is a revision statement.
type Revision struct {
	Date, Description, Reference string
	Stmt                         *Stmt
}

// Node is any parsed schema statement with a body: Kind is its keyword
// (container, leaf, leaf-list, list, choice, case, anydata, anyxml, uses,
// grouping, augment, refine, rpc, action, input, output, notification,
// typedef, feature, identity, extension, module, submodule). Name is the
// argument: the target node-id for augment/refine, the grouping for uses.
type Node struct {
	Kind, Name                     string
	Description, Reference, Status string
	IfFeatures                     []*IfFeature
	When                           *Restr
	Musts                          []*Restr
	Config, Mandatory              *bool
	Presence, Units                *string
	Defaults                       []string
	Type                           *Type
	Key                            *string
	Uniques                        []string
	MinElements, MaxElements       *uint32 // MaxElements 0 = unbounded
	OrderedBy                      string
	Bases                          []string // identity
	Argument                       string   // extension
	Children                       []*Node  // data definitions, cases
	Typedefs, Groupings            []*Node
	Actions, Notifications         []*Node
	Input, Output                  *Node
	Refines, Augments              []*Node
	Exts                           []*Stmt // extension instances, kept generic
	Stmt                           *Stmt
}

// Type is a type statement with its restrictions.
type Type struct {
	Name            string
	Range, Length   *Restr
	Patterns        []*Restr
	FractionDigits  uint8
	Enums, Bits     []*Enum
	Path            string
	RequireInstance *bool
	Bases           []string
	Types           []*Type // union members
	Exts            []*Stmt // exts array (OwnedExts): also those of path, fraction-digits, base, ...
	Stmt            *Stmt
}

// Restr is must, when, range, length or pattern. Exts is its exts array (OwnedExts): the
// instances in it and in its substatements.
type Restr struct {
	Arg, Description, Reference, ErrorMessage, ErrorAppTag string
	Invert                                                 bool // pattern modifier invert-match
	Exts                                                   []*Stmt
	Stmt                                                   *Stmt
}

// Enum is an enum or a bit; Value holds value or position. Exts is its exts array (OwnedExts).
type Enum struct {
	Name, Description, Reference, Status string
	Value                                *int64
	IfFeatures                           []*IfFeature
	Exts                                 []*Stmt
	Stmt                                 *Stmt
}

// Build returns the typed module of a tree from Parse (which made libyang's
// checks). It fails only on a root that is not a module or submodule.
func Build(s *Stmt) (*Module, error) {
	if s.ExtPrefix != "" || s.Keyword != "module" && s.Keyword != "submodule" {
		return nil, &Error{Pos: s.Pos, Code: ly.Syntax,
			Msg: "Invalid keyword \"" + s.Keyword + "\", expected \"module\" or \"submodule\"."}
	}
	b := &builder{v11: subArg(s, "yang-version") == "1.1"}
	m := &Module{Submodule: s.Keyword == "submodule"}
	b.node(&m.Node, s)
	for _, c := range s.Subs {
		if c.ExtPrefix != "" {
			continue
		}
		a := c.Arg
		switch c.Keyword {
		case "yang-version":
			m.Version = a
		case "namespace":
			m.Namespace = a
		case "prefix":
			m.Prefix = a
		case "belongs-to":
			m.BelongsTo, m.Prefix = a, subArg(c, "prefix")
		case "import":
			m.Imports = append(m.Imports, &Import{a, subArg(c, "prefix"), subArg(c, "revision-date"),
				subArg(c, "description"), subArg(c, "reference"), c})
		case "include":
			m.Includes = append(m.Includes, &Include{a, subArg(c, "revision-date"), subArg(c, "description"),
				subArg(c, "reference"), c})
		case "organization":
			m.Organization = a
		case "contact":
			m.Contact = a
		case "revision":
			m.Revisions = append(m.Revisions, &Revision{a, subArg(c, "description"), subArg(c, "reference"), c})
		case "extension":
			m.Extensions = append(m.Extensions, b.child(c))
		case "feature":
			m.Features = append(m.Features, b.child(c))
		case "identity":
			m.Identities = append(m.Identities, b.child(c))
		case "deviation":
			m.Deviations = append(m.Deviations, b.deviation(c))
		}
	}
	return m, nil
}

// builder holds the module-wide state of Build: if-feature expressions
// need the final YANG version, as lys_compile_iffeature sees it.
type builder struct{ v11 bool }

func subArg(s *Stmt, kw string) string {
	if c := subStmt(s, kw); c != nil {
		return c.Arg
	}
	return ""
}

func boolp(s string) *bool { v := s == "true"; return &v }

func strp(s string) *string { return &s }

func (b *builder) child(s *Stmt) *Node {
	n := &Node{}
	b.node(n, s)
	return n
}

// node fills n from the (already checked) statement s.
func (b *builder) node(n *Node, s *Stmt) {
	n.Kind, n.Name, n.Stmt = s.Keyword, s.Arg, s
	for _, c := range s.Subs {
		if c.ExtPrefix != "" {
			n.Exts = append(n.Exts, c)
			continue
		}
		a := c.Arg
		switch c.Keyword {
		case "description":
			n.Description = a
		case "reference":
			n.Reference = a
		case "status":
			n.Status = a
		case "if-feature":
			n.IfFeatures = append(n.IfFeatures, ifFeature(c, b.v11))
		case "when":
			n.When = restriction(c)
		case "must":
			n.Musts = append(n.Musts, restriction(c))
		case "config":
			n.Config = boolp(a)
		case "mandatory":
			n.Mandatory = boolp(a)
		case "presence":
			n.Presence = strp(a)
		case "units":
			n.Units = strp(a)
		case "default":
			n.Defaults = append(n.Defaults, a)
		case "type":
			n.Type = b.typ(c)
		case "key":
			n.Key = strp(a)
		case "unique":
			n.Uniques = append(n.Uniques, a)
		case "min-elements":
			v, _ := strconv.ParseUint(a, 10, 32)
			n.MinElements = new(uint32)
			*n.MinElements = uint32(v)
		case "max-elements":
			v, _ := strconv.ParseUint(a, 10, 32) // "unbounded" → 0
			n.MaxElements = new(uint32)
			*n.MaxElements = uint32(v)
		case "ordered-by":
			n.OrderedBy = a
		case "base":
			n.Bases = append(n.Bases, a)
		case "argument":
			n.Argument = a
		case "input":
			n.Input = b.child(c)
		case "output":
			n.Output = b.child(c)
		case "typedef":
			n.Typedefs = append(n.Typedefs, b.child(c))
		case "grouping":
			n.Groupings = append(n.Groupings, b.child(c))
		case "rpc", "action":
			n.Actions = append(n.Actions, b.child(c))
		case "notification":
			n.Notifications = append(n.Notifications, b.child(c))
		case "refine":
			n.Refines = append(n.Refines, b.child(c))
		case "augment":
			n.Augments = append(n.Augments, b.child(c))
		case "container", "leaf", "leaf-list", "list", "choice", "case", "anydata", "anyxml", "uses":
			n.Children = append(n.Children, b.child(c))
		}
	}
}

func restriction(s *Stmt) *Restr {
	r := &Restr{Arg: s.Arg, Exts: OwnedExts(s), Stmt: s}
	for _, c := range s.Subs {
		switch {
		case c.ExtPrefix != "":
		case c.Keyword == "description":
			r.Description = c.Arg
		case c.Keyword == "reference":
			r.Reference = c.Arg
		case c.Keyword == "error-message":
			r.ErrorMessage = c.Arg
		case c.Keyword == "error-app-tag":
			r.ErrorAppTag = c.Arg
		case c.Keyword == "modifier":
			r.Invert = true
		}
	}
	return r
}

func (b *builder) typ(s *Stmt) *Type {
	t := &Type{Name: s.Arg, Exts: OwnedExts(s), Stmt: s}
	for _, c := range s.Subs {
		if c.ExtPrefix != "" {
			continue
		}
		switch c.Keyword {
		case "range":
			t.Range = restriction(c)
		case "length":
			t.Length = restriction(c)
		case "pattern":
			t.Patterns = append(t.Patterns, restriction(c))
		case "fraction-digits":
			v, _ := strconv.ParseUint(c.Arg, 10, 8)
			t.FractionDigits = uint8(v)
		case "enum", "bit":
			e := &Enum{Name: c.Arg, Exts: OwnedExts(c), Stmt: c}
			for _, d := range c.Subs {
				switch {
				case d.ExtPrefix != "":
				case d.Keyword == "description":
					e.Description = d.Arg
				case d.Keyword == "reference":
					e.Reference = d.Arg
				case d.Keyword == "status":
					e.Status = d.Arg
				case d.Keyword == "if-feature":
					e.IfFeatures = append(e.IfFeatures, ifFeature(d, b.v11))
				case d.Keyword == "value" || d.Keyword == "position":
					v, _ := enumValue(d)
					e.Value = &v
				}
			}
			if c.Keyword == "enum" {
				t.Enums = append(t.Enums, e)
			} else {
				t.Bits = append(t.Bits, e)
			}
		case "path":
			t.Path = c.Arg
		case "require-instance":
			t.RequireInstance = boolp(c.Arg)
		case "base":
			t.Bases = append(t.Bases, c.Arg)
		case "type":
			t.Types = append(t.Types, b.typ(c))
		}
	}
	return t
}
