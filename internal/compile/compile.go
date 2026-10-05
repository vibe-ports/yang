// SPDX-License-Identifier: BSD-3-Clause

// Package compile turns parsed modules into a compiled schema (docs/design/06-compile.md).
package compile

import (
	"fmt"

	"github.com/vibe-ports/yang/internal/ly"
)

// vErr is an error libyang logs with LOGVAL: the return code (LY_ERR name, usually LY_EVALID),
// the validation code and the message; the caller adds the schema path of the node being
// compiled.
type vErr struct {
	Err  string
	Code ly.Code
	Msg  string
}

func (e *vErr) Error() string { return e.Msg }

func verr(code ly.Code, format string, a ...any) error {
	return &vErr{Err: "LY_EVALID", Code: code, Msg: fmt.Sprintf(format, a...)}
}
