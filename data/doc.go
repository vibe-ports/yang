// SPDX-License-Identifier: BSD-3-Clause

// Package data is the YANG data tree of the libyang v5.8.6 port (design 02, design 07): parsing
// JSON (RFC 7951) and XML (RFC 7950) instance data against a schema snapshot, validation, printing
// with the with-defaults modes (RFC 6243) and the tree edits.
//
// A tree is parsed against a *yang.Schema, the immutable snapshot a yang.Context returns from
// Schema(); the tree keeps that snapshot, so loading more modules later does not change it.
// Parse validates unless ParseOptions.ParseOnly is set, in libyang's order: every inner node when
// it closes (its new-node checks and implicit defaults), then the when conditions, the values
// that need the tree (leafref, instance-identifier) and the final checks (must, mandatory,
// min/max-elements, unique). Tree.Validate runs the same checks over a whole tree, and
// Tree.ValidateDiff also returns the implicit diff (the nodes the validation added or removed).
//
// Tree.FindXPath, Tree.EvalXPath and Tree.EvalXPathAs query a tree with XPath 1.0 and the YANG
// function library (RFC 7950 §10), as lyd_find_xpath and lyd_eval_xpath4: JSON-format expressions,
// an optional context node and variables, typed results.
//
// Diagnostics are yang.Diagnostic values in libyang's log order, with libyang's LY_ERR and
// LY_VECODE names, data path, schema path and error-app-tag. A failed call returns a
// *ValidationError holding them (RC is the call's LY_ERR name), or an error wrapping
// yang.ErrBudget when a Budget limit or one of libyang's nesting limits was hit. An invalid
// argument (libyang's LY_CHECK_ARG_RET / LOGARG) is no exception: it is an LY_EINVAL
// *ValidationError whose message is libyang's "Invalid argument <arg> (<function>())." with
// libyang's text of the check. Invalid input yields no tree, as libyang frees the whole tree on
// any error.
//
// A Schema may be shared by any number of goroutines. Read-only calls on a Tree may run
// concurrently: Find, FindXPath, EvalXPath, EvalXPathAs, the Node accessors and iterators,
// metadata lookups and printing. Anything that modifies a tree (NewPath, NewMeta, Remove, Validate,
// diff application, …) needs exclusive access: no other call on that tree, read or write, at the
// same time.
package data
