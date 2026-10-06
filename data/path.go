// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/tree_data.c (lyd_path and its predicate helpers) and
// src/tree_data_common.c (lyd_node_module, lyd_list_pos) (BSD-3-Clause, © CESNET).

package data

import (
	"strconv"
	"strings"

	"github.com/vibe-ports/yang/internal/schema"
	"github.com/vibe-ports/yang/internal/types"
)

// nodeModule is lyd_node_module: the module of n, for an opaque node the implemented module of
// its namespace or module name, else of its nearest ancestor that has one.
func nodeModule(set *schema.Set, n *Node) *schema.Module {
	for ; n != nil; n = n.parent {
		switch {
		case n.schema != nil:
			return n.schema.Module
		case n.opaq.ModuleNS == "":
		case n.opaq.Format == types.FormatXML:
			return set.ByNamespace(n.opaq.ModuleNS)
		default:
			return set.Implemented(n.opaq.ModuleNS)
		}
	}
	return nil
}

// listPos is lyd_list_pos: the 1-based position of n among the instances of its schema node.
func listPos(n *Node) int {
	sib := n.siblingsOf()
	if sib == nil {
		return 1
	}
	i := len(sib.list) - 1
	for i >= 0 && sib.list[i] != n {
		i--
	}
	if i < 0 {
		return 1 // not linked: the node alone
	}
	pos := 0
	for ; i >= 0 && sib.list[i].schema == n.schema; i-- {
		pos++
	}
	return pos
}

// quoted is the predicate value of lyd_path: in apostrophes, or in quotes when it contains one
// (no escaping: a value with both quote kinds gives an unparsable path, D-0052 candidate).
func quoted(v string) string {
	if strings.ContainsRune(v, '\'') {
		return `"` + v + `"`
	}
	return "'" + v + "'"
}

// lydPath is lyd_path(n, LYD_PATH_STD), or LYD_PATH_STD_NO_LAST_PRED with noLastPred: the
// module name on every module change, list keys `[k='v']`, config leaf-list values `[.='v']`,
// the position `[n]` of keyless list and state leaf-list instances. A list not linked to its
// parent yet (keys incomplete) starts the path.
func lydPath(set *schema.Set, n *Node, noLastPred bool) string {
	var chain []*Node
	for it := n; it != nil; it = it.parent {
		chain = append(chain, it)
	}
	var b strings.Builder
	for d := len(chain) - 1; d >= 0; d-- {
		it := chain[d]
		b.WriteByte('/')
		if mod := nodeModule(set, it); mod != nodeModule(set, it.parent) && mod != nil {
			b.WriteString(mod.Name)
			b.WriteByte(':')
		}
		b.WriteString(it.Name())
		if it.schema == nil || d == 0 && noLastPred {
			continue
		}
		switch it.schema.Kind {
		case schema.List:
			if it.schema.Keyless() {
				b.WriteString("[" + strconv.Itoa(listPos(it)) + "]")
				break
			}
			b.WriteString(listPredicate(it))
		case schema.LeafList:
			if it.schema.Config {
				b.WriteString("[.=" + quoted(it.value.Canonical()) + "]")
			} else {
				b.WriteString("[" + strconv.Itoa(listPos(it)) + "]")
			}
		}
	}
	return b.String()
}
