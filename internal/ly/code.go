// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/log.h (BSD-3-Clause, © CESNET).

// Package ly holds definitions shared by all ported packages, such as
// libyang's validation error codes.
package ly

import "strconv"

// Code is a libyang validation error code (LY_VECODE).
type Code uint8

// LY_VECODE values.
const (
	Success    Code = iota // LYVE_SUCCESS, also used for errors without a vecode (LOGERR)
	Syntax                 // LYVE_SYNTAX
	SyntaxYang             // LYVE_SYNTAX_YANG
	SyntaxYin              // LYVE_SYNTAX_YIN
	Reference              // LYVE_REFERENCE
	XPath                  // LYVE_XPATH
	Semantics              // LYVE_SEMANTICS
	SyntaxXML              // LYVE_SYNTAX_XML
	SyntaxJSON             // LYVE_SYNTAX_JSON
	Data                   // LYVE_DATA
	Other                  // LYVE_OTHER
)

var names = [...]string{"LYVE_SUCCESS", "LYVE_SYNTAX", "LYVE_SYNTAX_YANG", "LYVE_SYNTAX_YIN", "LYVE_REFERENCE",
	"LYVE_XPATH", "LYVE_SEMANTICS", "LYVE_SYNTAX_XML", "LYVE_SYNTAX_JSON", "LYVE_DATA", "LYVE_OTHER"}

// String returns libyang's name of the code (e.g. "LYVE_SYNTAX_YANG").
func (c Code) String() string {
	if int(c) < len(names) {
		return names[c]
	}
	return "LY_VECODE(" + strconv.Itoa(int(c)) + ")"
}
