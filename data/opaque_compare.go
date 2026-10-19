// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/tree_data.c, src/xml.c and src/ly_common.c (BSD-3-Clause, © CESNET).

package data

import (
	"strings"

	"github.com/vibe-ports/yang/internal/lyxml"
	"github.com/vibe-ports/yang/internal/schema"
	"github.com/vibe-ports/yang/internal/types"
)

// compareNodeValue is lyd_compare_single_value, including opaque XML values
// inside payload trees. Prefix spellings compare by their module and revision.
func compareNodeValue(a, b *Node) bool {
	if a.opaq != nil && b.opaq != nil && a.opaq.Format == types.FormatXML && b.opaq.Format == types.FormatXML {
		return compareXMLValue(a.opaq, b.opaq)
	}
	av, aok := compareValue(a)
	bv, bok := compareValue(b)
	return aok && bok && av == bv
}

func compareValue(n *Node) (string, bool) {
	v := n.Value()
	if n.opaq != nil && n.opaq.Format == types.FormatXML {
		if i := strings.IndexByte(v, ':'); i >= 0 {
			m := resolveOpaque(n.opaq, v[:i])
			if m == nil {
				return "", false
			}
			return m.Name + v[i:], true
		}
	}
	return v, true
}

func resolveOpaque(o *opaque, prefix string) *schema.Module {
	if o.Prefixes == nil {
		return nil
	}
	return o.Prefixes.Resolve(prefix)
}

// compareXMLValue is lyxml_value_compare. Unresolvable prefix-looking text is
// unequal, even when its spelling (or its unknown namespace URI) is identical.
func compareXMLValue(a, b *opaque) bool {
	i, j := 0, 0
	for i >= 0 && j >= 0 {
		an, ap, ai, aok := lyxml.ValuePrefixNext(a.Value, i)
		bn, bp, bi, bok := lyxml.ValuePrefixNext(b.Value, j)
		if !aok || !bok {
			// lyxml_value_compare stops on either scanner failure (xml.c:1373-1380),
			// and lyd_compare_single_value maps every non-success to LY_ENOT
			// (tree_data.c:1601-1605), including identical malformed bytes.
			return false
		}
		if ap != bp {
			return false
		}
		if ap {
			am, bm := resolveOpaque(a, a.Value[i:i+an]), resolveOpaque(b, b.Value[j:j+bn])
			if am == nil || bm == nil || am.Name != bm.Name || am.Revision != bm.Revision {
				return false
			}
		} else if an != bn || a.Value[i:i+an] != b.Value[j:j+bn] {
			return false
		}
		i, j = ai, bi
	}
	return i == j
}
