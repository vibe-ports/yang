// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/parser_yang.c, src/plugins_exts.c
// (lyplg_ext_parse_extension_instance) and src/parser_common.c (lys_parser_ext_instance_stmt,
// lysp_stmt_parse) (BSD-3-Clause, © CESNET).

package parser

import (
	"slices"

	"github.com/vibe-ports/yang/internal/ly"
)

// OwnedExts returns the exts array of s: its extension instances and those of its
// substatements that have no exts array of their own, in text order.
func OwnedExts(s *Stmt) []*Stmt { return appendOwned(nil, s, s.ExtPrefix != "") }

// ExtSubstmt is one substatement an extension plugin parses (lysp_ext_substmt): its keyword and
// whether it may occur more than once (a sized array, e.g. if-feature).
type ExtSubstmt struct {
	Keyword string
	Many    bool
}

// ParseExtInstance is lyplg_ext_parse_extension_instance for the plugin substatements subs (in
// the plugin's substmts order) of the extension instance ext: a YANG child that is not one of
// subs is rejected, then for each of subs in order every child with that keyword is checked for
// duplicates (lys_parser_ext_instance_stmt) and parsed with the checks of the YANG parser
// (lysp_stmt_parse). Nested extension instances are not children. The result holds the parsed
// substatements as a Node (Type, IfFeatures, Status, Units, ...; data definitions in Children,
// in subs order, then text order, as libyang links them). Errors carry no position:
// libyang reports them at the instance path.
func ParseExtInstance(ext *Stmt, subs []ExtSubstmt, v11 bool) (*Node, *Error) {
	arg := ""
	if ext.HasArg {
		arg = " " + ext.Arg
	}
	name := ext.ExtPrefix + ":" + ext.Keyword
	for _, s := range ext.Subs {
		known := s.ExtPrefix != ""
		for _, sub := range subs {
			known = known || s.Keyword == sub.Keyword
		}
		if !known {
			return nil, &Error{Code: ly.SyntaxYang,
				Msg: "Invalid keyword \"" + s.Keyword + "\" as a child of \"" + name + arg + "\" extension instance."}
		}
	}
	c := &checker{v11: v11, imports: map[string]string{}, extDefs: map[string]*Stmt{}}
	l := &lexer{chk: c}
	root := &frame{s: ext, live: true, seen: map[string]bool{}}
	for _, sub := range subs {
		seen := false
		for _, s := range ext.Subs {
			if s.ExtPrefix != "" || s.Keyword != sub.Keyword {
				continue
			}
			if seen && !sub.Many {
				return nil, &Error{Code: ly.SyntaxYang, Msg: "Duplicate keyword \"" + s.Keyword + "\"."}
			}
			seen = true
			if err := l.replay(root, s); err != nil {
				return nil, noPos(err)
			}
		}
	}
	n := &Node{}
	(&builder{v11: v11}).node(n, ext)
	// the parsed nodes are linked in the order they were parsed: per substatement of subs
	slices.SortStableFunc(n.Children, func(a, b *Node) int {
		return slices.IndexFunc(subs, func(s ExtSubstmt) bool { return s.Keyword == a.Kind }) -
			slices.IndexFunc(subs, func(s ExtSubstmt) bool { return s.Keyword == b.Kind })
	})
	return n, nil
}

// replay runs the checks lexer.stmt makes while reading s (a substatement of pf) over the
// already read tree: argument, each child keyword, the child, then the closing checks.
func (l *lexer) replay(pf *frame, s *Stmt) error {
	f := &frame{s: s, live: true, seen: map[string]bool{}}
	if err := validateValue(argOf(s.Keyword, pf.s.Keyword, false), s); err != nil {
		return err
	}
	if err := l.chk.arg(l, pf, s); err != nil {
		return err
	}
	for _, ch := range s.Subs {
		if ch.ExtPrefix != "" {
			continue // an extension instance: its content is generic
		}
		if err := l.chk.child(l, f, ch.Keyword, false); err != nil {
			return err
		}
		if err := l.replay(f, ch); err != nil {
			return err
		}
	}
	return l.chk.close(l, pf, f)
}

// validateValue is lysp_stmt_validate_value: the argument of a replayed substatement checked
// against its yang_arg class with the YANG lexer's character checks (the instance was read as
// generic extension content, where any string goes).
func validateValue(kind argKind, s *Stmt) error {
	if !s.HasArg {
		if kind == argMaybeStr || kind == argNone {
			return nil
		}
		return &Error{Code: ly.Syntax, Msg: "Missing an expected string."}
	}
	v := &lexer{src: []byte(s.Arg)}
	var prefix uint8
	for v.off < len(v.src) {
		c, n, ok := v.utf8At()
		if !ok {
			return v.errf(ly.Syntax, "Invalid character 0x%x.", v.c(0))
		}
		var err error
		switch kind {
		case argIdent:
			err = v.checkIdentChar(c, v.off == 0, nil)
		case argPrefIdent:
			err = v.checkIdentChar(c, v.off == 0, &prefix)
		default:
			if !isYangChar(c) {
				err = v.errf(ly.Syntax, "Invalid character 0x%x.", byte(c)) //nolint:gosec // (char)c, as libyang
			}
		}
		if err != nil {
			return err
		}
		v.off += n
	}
	return nil
}

func noPos(err error) *Error {
	e, ok := err.(*Error) //nolint:errorlint // the checker returns *Error only
	if !ok {
		return &Error{Code: ly.Other, Msg: err.Error()}
	}
	e.Pos = Pos{}
	return e
}
