// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/plugins.c (lyplg_type_plugin_find) (BSD-3-Clause, © CESNET).

package types

import "github.com/vibe-ports/yang/internal/schema"

// plugin is a type-specific handler for a typedef of a standard module (libyang
// lyplg_type_record), e.g. ietf-inet-types:ipv4-address.
type plugin struct {
	id      string // libyang plugin id without the "ly2 " prefix
	store   func(*storeArgs) (Value, *Diag)
	equal   func(a, b Value) bool // nil: canonical strings (lyplg_type_compare_simple)
	compare func(a, b Value) int  // nil: canonical strings (lyplg_type_sort_simple)
	// validate: the record's validate_value is lyplg_type_validate_value_string (hex-string);
	// the other records have none.
	validate bool
}

// pluginKey is (module, revision, typedef); revision "" matches every revision.
type pluginKey struct{ module, revision, name string }

var plugins = map[pluginKey]*plugin{}

// TypedefPlugin returns the identity of the handler record registered for the typedef name of
// module@revision, nil if there is none (lyplg_type_plugin_find). Compile compares identities for
// libyang's typedef reuse rule: a typedef with its own record is never merged into its base type.
// As libyang's records are per name, so are identities, also where names share one handler
// (hex-string, phys-address, …).
func TypedefPlugin(module, revision, name string) any {
	if k, _ := lookup(module, revision, name); k != nil {
		return *k
	}
	return nil
}

// Plugin returns the identity (as TypedefPlugin) of the record Store uses for t, nil for the
// built-in one of t.Base.
func Plugin(t *schema.Type) any {
	if k, _ := find(t); k != nil {
		return *k
	}
	return nil
}

// lookup finds the record of module@revision name, then the one for every revision.
func lookup(module, revision, name string) (*pluginKey, *plugin) {
	for _, k := range [...]pluginKey{{module, revision, name}, {module, "", name}} {
		if p := plugins[k]; p != nil {
			return &k, p
		}
	}
	return nil, nil
}

// find returns the record of the nearest typedef in t's derivation chain that has one, like
// lys_compile_type inheriting the plugin of the base typedef.
func find(t *schema.Type) (*pluginKey, *plugin) {
	for c := t; c != nil; c = c.From {
		if m := c.TypedefModule; m != nil && c.Typedef != "" {
			if k, p := lookup(m.Name, m.Revision, c.Typedef); p != nil {
				return k, p
			}
		}
	}
	return nil, nil
}

// pluginFor returns the handler Store uses for t, nil for the built-in one of t.Base.
func pluginFor(t *schema.Type) *plugin {
	_, p := find(t)
	return p
}
