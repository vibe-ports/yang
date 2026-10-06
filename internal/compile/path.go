// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/schema_compile.c (lysc_update_path) (BSD-3-Clause, © CESNET).

package compile

import (
	"strings"

	"github.com/vibe-ports/yang/internal/schema"
)

// lyscCtxBufsize is LYSC_CTX_BUFSIZE: longer compile paths are truncated.
const lyscCtxBufsize = 4078

// cpath is the log path of struct lysc_ctx (ctx->path, ctx->path_len); cur is ctx->cur_mod.
type cpath struct {
	b   [lyscCtxBufsize]byte
	n   int
	cur *schema.Module
}

func (p *cpath) init(cur *schema.Module) {
	p.cur, p.b[0], p.b[1], p.n = cur, '/', 0, 1
}

func (p *cpath) String() string { return string(p.b[:p.n]) }

// set replaces the path (strncpy into ctx->path, truncated like it).
func (p *cpath) set(s string) {
	p.n = copy(p.b[:lyscCtxBufsize-1], s)
	p.b[p.n] = 0
}

// pop is lysc_update_path(ctx, NULL, NULL): remove the last segment.
func (p *cpath) pop() {
	if p.b[p.n-1] == '}' {
		for p.b[p.n] != '=' && p.b[p.n] != '{' {
			p.n--
		}
		if p.b[p.n] == '=' {
			p.b[p.n] = '}'
			p.n++
			p.b[p.n] = 0
			return
		}
		// not a top-level special tag, remove also the preceding '/'
	}
	for p.b[p.n] != '/' {
		p.n--
	}
	if p.n == 0 {
		p.n = 1 // top-level (last segment)
	}
	p.b[p.n] = 0
}

// update is lysc_update_path with a name: parentMod nil is NULL (special segments, top level).
func (p *cpath) update(parentMod *schema.Module, name string) {
	next := 0 // 0 - no starttag, 1 - '/' starttag, 2 - '=' starttag + '}' endtag
	if p.n > 1 {
		if parentMod == nil && p.b[p.n-1] == '}' && p.b[p.n-2] != '\'' {
			next = 2 // extension of the special tag
			p.n--
		} else {
			next = 1
		}
	}
	var s string
	switch {
	case next == 2:
		s = "='" + name + "'}"
	case parentMod != nil && parentMod == p.cur || parentMod == nil && p.n > 1 && strings.HasPrefix(name, "{"):
		s = name // module not changed, print the name unprefixed
	default:
		s = p.cur.Name + ":" + name
	}
	if next == 1 {
		s = "/" + s
	}
	if avail := lyscCtxBufsize - p.n; len(s) >= avail {
		// snprintf output truncated
		copy(p.b[p.n:], s[:avail-1])
		p.n = lyscCtxBufsize - 1
	} else {
		copy(p.b[p.n:], s)
		p.n += len(s)
	}
	p.b[p.n] = 0
}
