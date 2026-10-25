// SPDX-License-Identifier: BSD-3-Clause

package data

import "github.com/vibe-ports/yang/internal/contextopts"

func init() {
	contextopts.NewImplicitNode = newImplicitNode
}

// newImplicitNode adapts the oracle's separate module/name arguments to the
// existing native constructors. builder's unqualified path selects the parent's
// context, and the actual name reaches the constructor without being split.
func newImplicitNode(tree, parent any, kind, name, value string, keys []string, o contextopts.NewImplicitOptions) error {
	t := tree.(*Tree)
	p, _ := parent.(*Node)
	defer t.work.beginHashWork()()
	var fn string
	switch kind {
	case "term":
		fn = "_lyd_new_term"
	case "inner":
		fn = "lyd_new_inner"
	case "list":
		fn = "lyd_new_list3"
	case "list2":
		fn = "lyd_new_list2"
	default:
		panic("data: unknown implicit constructor")
	}
	b, _, _, err := t.builder(p, "", fn)
	if err != nil {
		return err
	}
	vo := newValOptions{output: o.Output, storeOnly: o.StoreOnly, canon: o.Canonical}
	var n *Node
	switch kind {
	case "term":
		n, err = b.newTerm(p, nil, name, value, vo)
	case "inner":
		n, err = b.newInner(p, nil, name, o.Output)
	case "list":
		n, err = b.newList(p, nil, name, keys, vo, fn)
	case "list2":
		vo.canon = false // lyd_new_list2 ignores LYD_NEW_VAL_CANON.
		n, err = b.newList2(p, nil, name, value, vo)
	}
	_, err = t.placed(b, p, n, err)
	return err
}
