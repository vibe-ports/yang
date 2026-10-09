// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/diff.c (lyd_diff_reverse_all, lyd_diff_reverse_siblings_r,
// lyd_diff_reverse_value, lyd_diff_reverse_default, lyd_diff_reverse_meta,
// lyd_diff_rename_meta, lyd_diff_reverse_metadata_diff, lyd_diff_reverse_userord)
// (BSD-3-Clause, © CESNET).

package data

import (
	"fmt"
	"strconv"

	"github.com/vibe-ports/yang/internal/schema"
)

// ReverseDiff is lyd_diff_reverse_all: a new diff that undoes the diff t (create and delete
// swapped, replaced values and defaults back to their originals, user-ordered moves back to
// their original anchors, metadata operations reversed). A nil t gives nil. Errors are a
// *ValidationError with libyang's diagnostics.
func (t *Tree) ReverseDiff() (*Tree, error) {
	if t == nil || t.top.len() == 0 {
		return nil, nil
	}
	lg := &logger{set: t.set}
	d := newTree(t.set)
	if _, err := d.dupNodes(t.top.first(), nil, dupRecursive, true); err != nil {
		return nil, diffDone(lg, err)
	}
	if err := d.reverseSiblingsR(&d.top); err != nil {
		return nil, diffDone(lg, err)
	}
	return d, nil
}

// reverseValue is lyd_diff_reverse_value: a leaf's value and its yang:orig-value swapped, the
// default flag kept.
func (t *Tree) reverseValue(n *Node) error {
	oi := findYangMeta(n, "orig-value")
	if oi < 0 {
		return t.metaErr("orig-value", n)
	}
	m := n.meta[oi]
	cur := n.value.Canonical()
	flags := n.flags
	if rc := t.changeTerm(n, m.value.Canonical()); rc != "" {
		return &diffError{rc: rc} // LY_ENOT or LY_EEXIST returned, nothing logged
	}
	n.flags = flags
	return t.metaChange(n, m, cur)
}

// reverseDefault is lyd_diff_reverse_default: the default flag and yang:orig-default swapped.
func (t *Tree) reverseDefault(n *Node) error {
	i := findYangMeta(n, "orig-default")
	if i < 0 {
		return &opError{"LY_EINT", "Internal error."}
	}
	orig, cur := n.meta[i].value.Bool(), n.flags&FlagDefault != 0
	if orig == cur {
		return nil // no default state change to reverse
	}
	n.flags &^= FlagDefault
	if orig {
		n.flags |= FlagDefault
	}
	return t.metaChange(n, n.meta[i], strconv.FormatBool(cur))
}

// reverseMeta is lyd_diff_reverse_meta: the values of the metadata name1 and name2 swapped.
func (t *Tree) reverseMeta(n *Node, name1, name2 string) error {
	i1, i2 := findYangMeta(n, name1), findYangMeta(n, name2)
	if i1 < 0 {
		return t.metaErr(name1, n)
	}
	if i2 < 0 {
		return t.metaErr(name2, n)
	}
	m1, m2 := n.meta[i1], n.meta[i2]
	v1, v2 := m1.value.Canonical(), m2.value.Canonical()
	if err := t.metaChange(n, m1, v2); err != nil {
		return err
	}
	return t.metaChange(n, m2, v1)
}

// renameMeta is lyd_diff_rename_meta: the metadata src becomes trg (a new instance, at the end).
func (t *Tree) renameMeta(n *Node, src, trg string) error {
	i := findYangMeta(n, src)
	if i < 0 {
		return t.metaErr(src, n)
	}
	m := n.meta[i]
	if err := t.yangMeta(n, trg, m.value.Canonical()); err != nil {
		return err
	}
	delMeta(n, m)
	return nil
}

// reverseMetadataDiff is lyd_diff_reverse_metadata_diff: meta-create and meta-delete swapped,
// meta-replace and meta-orig swapped.
func (t *Tree) reverseMetadataDiff(n *Node) error {
	creates, deletes := metaOfName(n, "meta-create"), metaOfName(n, "meta-delete")
	repl, orig := metaOfName(n, "meta-replace"), metaOfName(n, "meta-orig")
	if err := alignReplaceOrig(repl, orig); err != nil {
		return err
	}
	for _, m := range creates {
		if err := t.metaNew(n, "meta-delete", m.value.Canonical()); err != nil {
			return err
		}
		delMeta(n, m)
	}
	for i, r := range repl {
		v := r.value.Canonical()
		if err := t.metaChange(n, r, orig[i].value.Canonical()); err != nil {
			return err
		}
		if err := t.metaChange(n, orig[i], v); err != nil {
			return err
		}
	}
	for _, m := range deletes {
		if err := t.metaNew(n, "meta-create", m.value.Canonical()); err != nil {
			return err
		}
		delMeta(n, m)
	}
	return nil
}

// reverseOrder is the order part of lyd_diff_reverse_userord: the collected instances of one
// user-ordered node put in reverse order where the last one is.
func (t *Tree) reverseOrder(nodes []*Node) error {
	last := len(nodes) - 1
	for _, n := range nodes[:last] {
		if err := unlinkTree(n); err != nil {
			return err
		}
	}
	anchor := nodes[last]
	for u := last - 1; u >= 0; u-- {
		if err := t.insertAfter(anchor, nodes[u]); err != nil {
			return err
		}
		anchor = nodes[u]
	}
	return nil
}

// reverseSiblingsR is lyd_diff_reverse_siblings_r: every node of sib reversed, recursively; the
// instances of a user-ordered node are collected and their order reversed once another
// user-ordered node starts, or at the end.
func (t *Tree) reverseSiblingsR(sib *siblings) error {
	var userord []*Node
	for _, n := range sib.nodes() {
		if n.isKey() {
			continue
		}
		op, err := t.diffOpOf(n)
		if err != nil {
			return err
		}
		uo := isUserOrdered(n.schema)
		name, orig := "", ""
		if uo {
			name, orig = anchorNames(n.schema)
		}
		switch op {
		case diffCreate, diffDelete:
			rev, from, to := diffDelete, name, orig
			if op == diffDelete {
				rev, from, to = diffCreate, orig, name
			}
			if err := t.diffChangeOp(n, rev); err != nil {
				return err
			}
			if uo {
				if err := t.renameMeta(n, from, to); err != nil {
					return err
				}
			}
			for _, c := range n.kids.nodes() { // the children keep the operation, handled below
				if c.isKey() {
					continue
				}
				if err := t.diffChangeOp(c, op); err != nil {
					return err
				}
			}
		case diffReplace:
			switch n.schema.Kind {
			case schema.Leaf:
				if err := t.reverseValue(n); err != nil {
					return err
				}
				if err := t.reverseDefault(n); err != nil {
					return err
				}
			case schema.AnyData, schema.AnyXML: // their values are M5 (U-0043)
				return fmt.Errorf("data: reversing a replace of %s %q: %w", nodetypeStr(n.schema.Kind), n.Name(), ErrUnsupported)
			case schema.LeafList:
				if err := t.reverseDefault(n); err != nil {
					return err
				}
				if err := t.reverseMeta(n, orig, name); err != nil {
					return err
				}
			case schema.List:
				if err := t.reverseMeta(n, orig, name); err != nil {
					return err
				}
			default:
				return &opError{"LY_EINT", "Internal error."}
			}
		case diffNone:
			if n.isTerm() {
				if err := t.reverseDefault(n); err != nil {
					return err
				}
			}
		}
		if err := t.reverseMetadataDiff(n); err != nil {
			return err
		}
		if err := t.reverseSiblingsR(&n.kids); err != nil {
			return err
		}
		if uo {
			if len(userord) > 0 && userord[0].schema != n.schema {
				if err := t.reverseOrder(userord); err != nil {
					return err
				}
				userord = nil
			}
			userord = append(userord, n)
		}
	}
	if len(userord) > 0 {
		return t.reverseOrder(userord)
	}
	return nil
}
