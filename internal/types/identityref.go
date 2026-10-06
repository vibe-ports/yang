// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/plugins_types/identityref.c and src/plugins_types.c
// (lyplg_type_identity_isderived) (BSD-3-Clause, © CESNET).

package types

import (
	"fmt"
	"strings"

	"github.com/vibe-ports/yang/internal/schema"
)

// storeIdentityRef ports lyplg_type_store_identityref (identityref_str2ident,
// identityref_check_ident, identityref_check_base).
func storeIdentityRef(a *storeArgs) (Value, *Diag) {
	if _, d := checkHints(a.h, a.lex, a.t.Base); d != nil {
		return Value{}, d
	}
	prefix, name := "", a.lex
	if c := strings.IndexByte(a.lex, ':'); c >= 0 {
		prefix, name = a.lex[:c], a.lex[c+1:]
	}
	if name == "" {
		return Value{}, errf("Invalid empty identityref value.")
	}
	mod := resolveModule(prefix, a.f, a.pc, a.ctx)
	if mod == nil {
		return Value{}, errf("Invalid identityref \"%s\" value - unable to map prefix to YANG schema.", a.lex)
	}
	id := mod.Identity(name)
	switch {
	case id == nil:
		return Value{}, errf("Invalid identityref \"%s\" value - identity not found in module \"%s\".", a.lex, mod.Name)
	case !mod.Implemented && a.impl != nil: // lyplg_type_make_implemented
		if err := a.impl(mod, false); err != nil {
			return Value{}, &Diag{Code: CodeData}
		}
	case !mod.Implemented:
		return Value{}, errf("Invalid identityref \"%s\" value - identity found in non-implemented module \"%s\".", a.lex, mod.Name)
	case id.Disabled:
		return Value{}, errf("Invalid identityref \"%s\" value - identity is disabled by if-feature.", a.lex)
	}
	if d := checkBases(a.t, id, a.lex); d != nil {
		return Value{}, d
	}
	if a.ctx != nil && a.f == FormatSchema && a.pc != nil {
		valMod := a.pc.Resolve("")
		if d := checkStatus(a.ctx, valMod, id.Status, id.Name, a.ctx.Module == valMod); d != nil {
			return Value{}, d
		}
	}
	v := Value{typ: a.t, ident: id, canon: id.Module.Name + ":" + id.Name}
	if a.f == FormatCanon {
		v.canon = a.lex
	}
	return v, nil
}

// checkBases ports identityref_check_base: the identity must be derived from one of the bases.
func checkBases(t *schema.Type, id *schema.Identity, lex string) *Diag {
	for _, b := range t.Bases {
		if IsDerived(b, id) {
			return nil
		}
	}
	names := make([]string, len(t.Bases))
	for i, b := range t.Bases {
		names[i] = fmt.Sprintf("\"%s:%s\"", b.Module.Name, b.Name)
	}
	if len(t.Bases) == 1 {
		return errf("Invalid identityref \"%s\" value - identity not derived from the base %s.", lex, names[0])
	}
	return errf("Invalid identityref \"%s\" value - identity not derived from all the bases %s.", lex, strings.Join(names, ", "))
}

// IsDerived reports whether der is derived (directly or not) from base, not base itself
// (lyplg_type_identity_isderived; XPath derived-from() uses it too).
func IsDerived(base, der *schema.Identity) bool {
	for _, d := range base.Derived {
		if d == der || IsDerived(d, der) {
			return true
		}
	}
	return false
}
