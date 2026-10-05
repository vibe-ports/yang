// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/plugins.c (lyplg_type_plugin_find) (BSD-3-Clause, © CESNET).

package types

import "github.com/vibe-ports/yang/internal/schema"

// plugin is a type-specific handler for a typedef of a standard module (libyang
// lyplg_type_record), e.g. ietf-inet-types:ipv4-address.
type plugin struct {
	id    string // libyang plugin id without the "ly2 " prefix
	store func(*storeArgs) (Value, *Diag)
}

// pluginKey is (module, revision, typedef); revision "" matches every revision.
type pluginKey struct{ module, revision, name string }

var plugins = map[pluginKey]*plugin{}

// pluginFor returns the handler of the nearest typedef in t's derivation chain that has one,
// like lys_compile_type inheriting the plugin of the base typedef.
func pluginFor(t *schema.Type) *plugin {
	for c := t; c != nil; c = c.From {
		m := c.TypedefModule
		if m == nil || c.Typedef == "" {
			continue
		}
		if p := plugins[pluginKey{m.Name, m.Revision, c.Typedef}]; p != nil {
			return p
		}
		if p := plugins[pluginKey{m.Name, "", c.Typedef}]; p != nil {
			return p
		}
	}
	return nil
}
