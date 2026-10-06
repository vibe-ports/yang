// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/parser_yang.c and src/parser_common.c (BSD-3-Clause, © CESNET).

package parser

// ExtOwners returns the statements owning a non-empty exts array, in libyang's ctx->ext_inst
// order (owners as they close): the arrays lysp_resolve_ext_instance_records walks.
func ExtOwners(root *Stmt) []*Stmt {
	var out []*Stmt
	var walk func(s *Stmt)
	walk = func(s *Stmt) {
		for _, c := range s.Subs {
			if c.ExtPrefix != "" || s.ExtPrefix == "" {
				walk(c)
			}
		}
		if (s.ExtPrefix != "" || extOwner[s.Keyword]) && len(OwnedExts(s)) > 0 {
			out = append(out, s)
		}
	}
	walk(root)
	return out
}

// OwnedExts returns the exts array of s: its extension instances and those of its
// substatements that have no exts array of their own, in text order.
func OwnedExts(s *Stmt) []*Stmt { return appendOwned(nil, s, s.ExtPrefix != "") }

// BuildType builds a type statement found in an extension instance (lysp_stmt_parse of
// LY_STMT_TYPE for an extension plugin); v11 is the module's YANG version.
func BuildType(s *Stmt, v11 bool) *Type { return (&builder{v11: v11}).typ(s) }

// BuildIfFeature builds an if-feature statement found in an extension instance.
func BuildIfFeature(s *Stmt, v11 bool) *IfFeature { return ifFeature(s, v11) }
