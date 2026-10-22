// SPDX-License-Identifier: BSD-3-Clause

package conformance

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/vibe-ports/yang"
)

// requestRule is one ordered check in oracle/lyoracle.c. Subtables preserve the
// C call boundaries: check_step runs for ALL steps before execution; input_of
// runs in its caller's phase (including step_diff during execution). Validation
// does not imply that the public API supports executing the request
// (ErrUnsupported remains a separate outcome).
type requestRule struct {
	line  int
	check func(*requestCheck) error
}

// executionContext is chosen by the top-level or sequence-step dispatcher. It is never inferred
// from an unrelated request field: root operations have no keys_only check, so fields such as
// "do" are ignored there (lyoracle.c:3273-3295), while check_step owns "do" for sequence steps
// (lyoracle.c:1700-1725, 2737-2769).
type executionContext uint8

const (
	execUnknown executionContext = iota
	execSchema
	execData
	execXPath
	execDiff
	execSequence
	execAtoms
	execStepParse
	execStepValidate
	execStepEdit
	execStepChangeTerm
	execStepDump
	execStepDup
	execStepInsert
	execStepCompare
	execStepDiff
	execStepDiffParse
	execStepDiffMerge
	execStepDiffApply
	execStepDiffReverse
	execStepTrim
	execStepXPath
	execStepLink
	execStepLinks
	execStepPrintXML
	execStepAnyDirect
	execStepUnlink
	execStepFindMeta
)

var rootExecutionContexts = map[string]executionContext{
	"schema": execSchema, "data": execData, "xpath": execXPath, "diff": execDiff,
	"sequence": execSequence, "atoms": execAtoms,
}

var stepExecutionContexts = map[string]executionContext{
	"parse": execStepParse, "validate": execStepValidate, "edit": execStepEdit,
	"change_term": execStepChangeTerm, "dump": execStepDump, "detached_dump": execStepDump, "dup": execStepDup,
	"insert": execStepInsert, "compare": execStepCompare, "diff": execStepDiff,
	"diff_parse": execStepDiffParse, "diff_merge": execStepDiffMerge,
	"diff_apply": execStepDiffApply, "diff_reverse": execStepDiffReverse,
	"trim": execStepTrim, "link": execStepLink, "links": execStepLinks,
	"print_xml": execStepPrintXML, "any_direct": execStepAnyDirect,
	"xpath":  execStepXPath,
	"unlink": execStepUnlink, "find_meta": execStepFindMeta,
}

func (x executionContext) name() string {
	for name, ctx := range rootExecutionContexts {
		if x == ctx {
			return name
		}
	}
	for name, ctx := range stepExecutionContexts {
		if x == ctx {
			return name
		}
	}
	return ""
}

type requestCheck struct {
	r      Request
	p      map[string]any
	schema *yang.Schema
	inputs map[string]bool
	exec   executionContext
}

func (c *requestCheck) run(rows []requestRule) error {
	for _, row := range rows {
		if err := row.check(c); err != nil {
			return err
		}
	}
	return nil
}

func requestError(format string, args ...any) error {
	return fmt.Errorf(format+" (a request-error)", args...)
}

func requestStrings(keys ...string) func(*requestCheck) error {
	return func(c *requestCheck) error {
		for _, k := range keys {
			if _, _, err := strField(c.p, k); err != nil {
				return err
			}
		}
		return nil
	}
}

func requestBools(keys ...string) func(*requestCheck) error {
	return func(c *requestCheck) error {
		for _, k := range keys {
			if v, exists := c.p[k]; exists {
				if _, ok := v.(bool); !ok {
					switch c.exec {
					case execStepDiff:
						return requestError("diff single, merge, and detached must be booleans")
					case execStepDup, execStepChangeTerm, execStepInsert, execStepLinks, execStepXPath:
						return requestError("%s %s must be a boolean", c.exec.name(), k)
					}
					if k == "xml" {
						return requestError("insert_opaq xml must be a boolean")
					}
					return requestError("%s must be a boolean", k)
				}
			}
		}
		return nil
	}
}

func requestFlags(key, allowed string) func(*requestCheck) error {
	return func(c *requestCheck) error {
		v, exists := c.p[key]
		if !exists {
			return nil
		}
		a, ok := v.([]any)
		if !ok {
			return requestError("field %q must be an array of flags", key)
		}
		flags := set(strings.Fields(allowed)...)
		for _, v := range a {
			s, ok := v.(string)
			if !ok || !flags[s] {
				return requestError("unknown flag in %q", key)
			}
		}
		return nil
	}
}

func requestEnum(key, fallback, allowed string) func(*requestCheck) error {
	return func(c *requestCheck) error {
		v := str(c.p, key, fallback)
		for _, s := range strings.Fields(allowed) {
			if s == v {
				return nil
			}
		}
		return requestError("unknown %s %s", key, v)
	}
}

// requestInput is input_of (lyoracle.c:257-267), including str_of's null
// semantics and the conflict check BEFORE reading the file. Empty strings are
// present values. A missing input is distinct from an empty input.
func requestInput(r Request, p map[string]any, key string) (string, bool, error) {
	s, hasS, err := strField(p, key)
	if err != nil {
		return "", false, err
	}
	file, hasFile, err := strField(p, key+"_file")
	if err != nil {
		return "", false, err
	}
	if hasS && hasFile {
		return "", false, requestError("both inline and _file given for %q", key)
	}
	if !hasFile {
		return s, hasS, nil
	}
	path := file
	if !filepath.IsAbs(path) {
		path = filepath.Join(r.BaseDir, path)
	}
	b, err := os.ReadFile(path) //nolint:gosec // fixture path, as in the oracle
	if err != nil {
		return "", false, requestError("cannot read file %s: %v", file, err)
	}
	return string(b), true, nil
}

func requestRead(key string, required bool) func(*requestCheck) error {
	return func(c *requestCheck) error {
		_, present, err := requestInput(c.r, c.p, key)
		if err != nil {
			return err
		}
		c.inputs[key] = present
		if required && !present {
			return requestError("missing %s", key)
		}
		return nil
	}
}

func requestParams(c *requestCheck) error { return c.run(dparamsRules) }
func requestOperation(c *requestCheck) bool {
	switch str(c.p, "data_type", "data-operational") {
	case "rpc", "reply", "notif", "rpc-netconf", "notif-netconf", "reply-netconf", "rpc-restconf", "notif-restconf", "reply-restconf":
		return true
	}
	return false
}

// envelopeType reports a NETCONF or RESTCONF message data type (LYD_TYPE_*_NETCONF/_RESTCONF).
func envelopeType(t string) bool {
	return strings.HasSuffix(t, "-netconf") || strings.HasSuffix(t, "-restconf")
}
func requestDatastore(c *requestCheck) error {
	if requestOperation(c) {
		return requestError("sequence supports datastore data types only")
	}
	return nil
}
func requestParseOnly(c *requestCheck) error {
	if requestOperation(c) && c.p["parse_only"] != true {
		if c.exec == execStepDiff {
			return requestError("sequence diffs operation data with parse_only only")
		}
		return requestError("sequence parses operation data with parse_only only")
	}
	return nil
}
func requestReply(c *requestCheck) error {
	if c.inputs["rpc"] && str(c.p, "data_type", "") != "reply" {
		return requestError("%s step: rpc only with data_type reply", c.exec.name())
	}
	return nil
}
func requestParent(c *requestCheck) error {
	if c.p["rpc_parent"] != nil && (!c.inputs["rpc"] || c.p["keep_input"] != true || str(c.p, "data_type", "") != "rpc") {
		return requestError("rpc_parent requires data_type rpc, rpc, and keep_input true")
	}
	return nil
}

var dparamsRules = []requestRule{
	{1210, requestStrings("data_type", "unknown")},                 // caller phase: request preflight or execution; lyoracle.c:1210 str_of
	{1214, requestStrings("format")},                               // caller phase: request preflight or execution; lyoracle.c:1214 str_of before fmt_of
	{1214, requestEnum("format", "json", "json xml")},              // caller phase: request preflight or execution; lyoracle.c:1214 fmt_of
	{1216, requestEnum("unknown", "reject", "reject skip opaque")}, // caller phase: request preflight or execution; lyoracle.c:1216 enum_of
	// parse_only and keep_input use cJSON_IsTrue at 1219-1220: no type check.
	{1221, requestStrings("rpc_parent")}, // caller phase: request preflight or execution; lyoracle.c:1221 str_of
	{1223, requestEnum("data_type", "data-operational", "config data-operational data get getconfig edit rpc reply notif rpc-netconf notif-netconf reply-netconf rpc-restconf notif-restconf reply-restconf")}, // caller phase: request preflight or execution; lyoracle.c:1223-1245
	{1250, func(c *requestCheck) error { // caller phase: request preflight or execution; lyoracle.c:1250-1252
		t := str(c.p, "data_type", "")
		if envelopeType(t) && c.p["parse_only"] != true {
			return requestError("data_type %s needs parse_only", t)
		}
		return nil
	}},
	{1254, requestFlags("parse_options", "only strict opaq no_state ordered when_true store_only json_null json_string_datatypes anydata_strict")}, // caller phase: request preflight or execution; lyoracle.c:1254 flags_of
	{1255, func(c *requestCheck) error { // caller phase: request preflight or execution; lyoracle.c:1255-1256
		for _, v := range list(c.p["parse_options"]) {
			if v == "strict" || v == "opaq" {
				return requestError("parse_options strict/opaq are replaced by unknown")
			}
		}
		return nil
	}},
	{1259, requestFlags("validate_options", "no_state present multi_error operational no_defaults not_final")}, // caller phase: request preflight or execution; lyoracle.c:1259 flags_of
}

// op_data has no keys_only: unrelated fields (including module/final_only) are
// ignored. The prelude and body are separate because load_oper can return a
// verdict before the body's inputs are read.
var dataPreludeRules = []requestRule{
	{1429, requestParams},                        // request prelude; lyoracle.c:1429 dparams_of
	{1400, requestRead("operational", false)},    // request prelude; lyoracle.c:1400 load_oper/input_of
	{1401, requestStrings("operational_format")}, // request prelude; lyoracle.c:1401 str_of
}
var dataInitRules = []requestRule{
	{1441, requestStrings("print_subtree")},                                                          // request prelude; lyoracle.c:1441 str_of
	{1442, requestStrings("with_defaults")},                                                          // request prelude; lyoracle.c:1442 wd_of
	{1442, requestEnum("with_defaults", "explicit", "explicit trim all all-tagged implicit-tagged")}, // request prelude; lyoracle.c:1442 wd_of
	{1442, requestFlags("print_options", "shrink empty_leaf_list empty_cont json_no_nested_prefix")}, // request prelude; lyoracle.c:1442 flags_of
}
var dataRequestRules = []requestRule{
	{1448, requestRead("data", true)}, // request prelude; lyoracle.c:1448 input_of
	{1451, requestRead("rpc", false)}, // request prelude; lyoracle.c:1451 input_of before parse_one, even for datastore
	{1281, requestParent},             // execution phase; lyoracle.c:1281 parse_one (before parsing)
}
var diffRequestRules = []requestRule{
	{1645, requestRead("first", false)},  // request prelude; lyoracle.c:1645 input_of
	{1646, requestRead("second", false)}, // request prelude; lyoracle.c:1646 input_of
	{1647, func(c *requestCheck) error { // request prelude; lyoracle.c:1647
		if !c.inputs["first"] || !c.inputs["second"] {
			return requestError("diff needs first and second")
		}
		return nil
	}},
	{1650, func(c *requestCheck) error {
		if requestOperation(c) {
			return requestError("diff supports datastore data types only")
		}
		return nil
	}}, // request prelude; lyoracle.c:1650
	{1281, requestParent}, // execution phase; lyoracle.c:1281 parse_one, called at 1653 with a NULL rpc_src
}

var sequenceRequestRules = []requestRule{
	{2900, func(c *requestCheck) error { // request phase; lyoracle.c:3013
		if _, ok := c.p["steps"].([]any); !ok {
			return requestError("sequence needs a steps array")
		}
		return nil
	}},
}
var stepEntryRules = []requestRule{
	{1700, requestStrings("do")}, // request phase; lyoracle.c:1700
	{1703, func(c *requestCheck) error { // request phase; lyoracle.c:1703
		if c.p == nil || c.p["do"] == nil {
			return requestError("step without do")
		}
		return nil
	}},
	{1706, func(c *requestCheck) error { // request phase; lyoracle.c:1706-1725 keys_only, unknown kinds allow only do
		keys := seqKeys[str(c.p, "do", "")]
		if keys == nil {
			keys = set("do")
		}
		for _, k := range slices.Sorted(maps.Keys(c.p)) {
			if !keys[k] {
				return requestError("unknown key %q in a sequence step", k)
			}
		}
		return nil
	}},
}
var stepRequestRules = map[string][]requestRule{
	"edit": {
		{1728, requestEditKeys},             // request phase; lyoracle.c:1728-1739
		{1768, requestRead("merge", false)}, // request phase; lyoracle.c:1768
		{1768, requestStrings("delete")},    // request phase; lyoracle.c:1768
		{1778, requestRead("merge_into", false)},
		{1780, requestEdit}, // request phase; lyoracle.c:1780-1860 nested edit objects
	},
	"change_term": {
		{1867, requestChangeSelector}, // request phase; lyoracle.c:1867
		{1879, requestBools("canon")}, // request phase; lyoracle.c:1879
	},
	"dump": {
		{1883, requestStrings("with_defaults")},                                                          // request phase; lyoracle.c:1883 wd_of
		{1883, requestEnum("with_defaults", "explicit", "explicit trim all all-tagged implicit-tagged")}, // request phase; lyoracle.c:1883
	},
	"detached_dump": {
		{1883, requestStrings("with_defaults")},
		{1883, requestEnum("with_defaults", "explicit", "explicit trim all all-tagged implicit-tagged")},
		{1883, requestBools("child")},
	},
	"print_xml": {{1884, requestNoop}},
	"any_direct": {
		{1888, requestRequired("any_direct needs a case", "case")},
		{1898, func(c *requestCheck) error { // lyoracle.c:1898 has its own wording
			v := str(c.p, "case", "")
			for _, s := range strings.Fields("nil_boundaries text_nul node_then_empty equal_repeated_collisions equal_mixed_parent_payload dup_payload_parent_context trim_payload_links merge_mixed_into_tree dup_siblings_into_empty_anydata dup_siblings_into_empty_anyxml dup_into_default_any_parent") {
				if s == v {
					return nil
				}
			}
			return requestError("unknown any_direct case %s", v)
		}},
		{1887, func(c *requestCheck) error {
			if v, ok := c.p["target"]; ok {
				tg, good := v.(map[string]any)
				if !good {
					return requestError("any_direct target must be a context object")
				}
				return requestKeys(tg, "searchdirs modules context_options")
			}
			return nil
		}},
	},
	"dup": {
		{1911, requestDupSelector},       // request phase; lyoracle.c:1911
		{1918, requestStrings("parent")}, // request phase; lyoracle.c:1918
		{1919, requestFlags("options", "recursive no_meta with_parents with_flags no_lyds")}, // request phase; lyoracle.c:1919
		{1920, requestBools("siblings", "detached")},                                         // request phase; lyoracle.c:1920
		{1926, requestDupTarget},                                                             // request phase; lyoracle.c:1926-1932
		{1935, requestDupSource},                                                             // request phase; lyoracle.c:1934-1942
	},
	"trim": {
		{2056, requestRequired("trim step needs an xpath", "xpath")}, // request phase; lyoracle.c:2056
		{2059, requestVars}, // request phase; lyoracle.c:2059-2066
	},
	"xpath": {
		{2070, requestRequired("xpath step needs an xpath", "xpath")}, // request phase; lyoracle.c:2070
		{2073, requestStrings("node")},
		{2074, func(c *requestCheck) error {
			if _, present, _ := strField(c.p, "node"); present && c.p["detached"] == true {
				return requestError("xpath node and detached are mutually exclusive")
			}
			return nil
		}},
		{2077, requestBools("detached")},
		{2080, requestVars}, // request phase; lyoracle.c:2080-2088
	},
	"link":         {{2104, requestNoop}},              // request phase; lyoracle.c:2104 unknown-kind tail
	"links":        {{2098, requestBools("detached")}}, // request phase; lyoracle.c:2098 links selector
	"diff_reverse": {{2104, requestNoop}},              // request phase; lyoracle.c:2104 unknown-kind tail
	"unlink": {
		{2090, requestStrings("node", "diff_node")},
		{2091, func(c *requestCheck) error {
			_, nodePresent, _ := strField(c.p, "node")
			_, diffPresent, _ := strField(c.p, "diff_node")
			if nodePresent == diffPresent {
				return requestError("unlink step needs exactly one node or diff_node path")
			}
			return nil
		}},
	},
	"find_meta": {
		{2094, requestStrings("node")},
		{2095, requestRequired("find_meta step needs a name", "name")},
	},

	"parse": {
		{1742, requestParams},             // request phase; lyoracle.c:1742
		{1743, requestParseOnly},          // request phase; lyoracle.c:1743
		{1747, requestRead("rpc", false)}, // request phase; lyoracle.c:1747
		{1747, requestReply},              // request phase; lyoracle.c:1747
		{1750, requestStepData},           // request phase; lyoracle.c:1750
	},
	"validate": {
		{1755, requestStrings("module")},   // request phase; lyoracle.c:1755
		{1757, requestParams},              // request phase; lyoracle.c:1757
		{1758, requestBools("final_only")}, // request phase; lyoracle.c:1758 null is NOT a bool
		{1761, func(c *requestCheck) error { // request phase; lyoracle.c:1761
			if c.p["final_only"] == true && c.p["module"] == nil {
				return requestError("final_only needs module")
			}
			return nil
		}},
		{1764, func(c *requestCheck) error { // request phase; lyoracle.c:1764 original context, before any step executes
			if c.p["module"] != nil && c.schema.Implemented(str(c.p, "module", "")) == nil {
				return requestError("validate module %s not implemented", c.p["module"])
			}
			return nil
		}},
	},
	"insert": {
		{1947, requestStrings("node", "parent")}, // request phase; lyoracle.c:1947-1948
		{1949, requestBools("detached")},
		{1952, requestRead("data", false)}, // request phase; lyoracle.c:1952
		{1953, func(c *requestCheck) error { // request phase; lyoracle.c:1953-1967
			if c.inputs["data"] {
				if c.p["detached"] == true {
					return requestError("insert step takes data or detached, not both")
				}
				if err := requestParams(c); err != nil {
					return err
				}
				return requestDatastore(c)
			}
			if c.p["node"] == nil && c.p["detached"] != true {
				return requestError("insert step needs a node, data, or detached")
			}
			return nil
		}},
		{1960, requestInsertTarget},
	},
	"compare": {
		{1974, func(c *requestCheck) error { // request phase; lyoracle.c:1974-1984
			if c.p["self"] == true {
				return requestKeys(c.p, "do self first second options")
			}
			if err := requestParams(c); err != nil {
				return err
			}
			if err := requestDatastore(c); err != nil {
				return err
			}
			return requestCompareInput(c)
		}},
		{1985, requestStrings("first", "second")},                       // request phase; lyoracle.c:1985-1986
		{1987, requestFlags("options", "full_recursion defaults opaq")}, // request phase; lyoracle.c:1987
		{1988, requestCompareTarget},                                    // request phase; lyoracle.c:1988-1992
	},
	"diff": {
		{1971, requestParams},             // request phase; lyoracle.c:1952
		{1987, requestRead("rpc", false)}, // request phase; lyoracle.c:1982 BEFORE the parse_only check
		{1995, requestParseOnly},          // request phase; lyoracle.c:1984
		{1999, requestReply},              // request phase; lyoracle.c:1985
		{2002, func(c *requestCheck) error { // request phase; lyoracle.c:1994 presence, not IsTrue
			if _, exists := c.p["keep_input"]; exists && !c.inputs["rpc"] {
				return requestError("diff step: keep_input only with rpc")
			}
			return nil
		}},
		{2006, requestFlags("options", "defaults meta")},    // request phase; lyoracle.c:1998
		{2007, requestFlags("merge_options", "defaults")},   // request phase; lyoracle.c:1999
		{2022, requestStrings("node", "data_node")},         // request phase; lyoracle.c:2014-2015
		{2010, requestTarget},                               // request phase; lyoracle.c:2002-2012
		{2022, requestBools("single", "merge", "detached")}, // request phase; lyoracle.c:2014-2016
	},
	"diff_parse": {
		{1971, requestParams},    // request phase; lyoracle.c:1952
		{1974, requestDatastore}, // request phase; lyoracle.c:1956
		{1984, requestStepData},  // request phase; lyoracle.c:1959
	},
	"diff_merge": {
		{1971, requestParams},                       // request phase; lyoracle.c:1952
		{1974, requestDatastore},                    // request phase; lyoracle.c:1956
		{1984, requestStepData},                     // request phase; lyoracle.c:1959
		{2042, requestFlags("options", "defaults")}, // request phase; lyoracle.c:2042
		{2042, requestBools("detached_parent")},
		{2043, requestStrings("module")}, // request phase; lyoracle.c:2043
		{2044, requestMergeSelectors},    // request phase; lyoracle.c:2044-2049
	},
	"diff_apply": {{2052, requestStrings("module")}}, // request phase; lyoracle.c:2052
}

// These checks happen only when a step executes. In particular, diff.data is
// not read by check_step unless target is present, and module_of uses the
// retained tree's context, unlike validate's preflight lookup.
var stepExecutionRules = map[string][]requestRule{
	"dup": {{2129, func(c *requestCheck) error { // step_dup: source dparams_of runs after preflight and before source input/selection
		if source, ok := c.p["source"].(map[string]any); ok {
			c.p = source
			return requestParams(c)
		}
		return nil
	}}},
	"diff":       {{2528, requestRead("data", false)}}, // execution phase; lyoracle.c:2523 step_diff
	"diff_merge": {{2619, requestModule}},              // execution phase; lyoracle.c:2614 module_of
	"diff_apply": {{3014, requestModule}},              // execution phase; lyoracle.c:3009 module_of
	"any_direct": {{2322, requestAnyDirectTarget}},     // execution phase; lyoracle.c:2322,3404 before build_ctx_in
}

func requestAnyDirectTarget(c *requestCheck) error {
	which := str(c.p, "case", "")
	// step_any_direct constructs its first ac-r6 node before it calls
	// check_equal_order_contexts, where the target is consumed
	// (lyoracle.c:2289-2315, 3211-3219). A missing module returns LY_EINVAL
	// first, leaving the target request check unreachable.
	if which == "equal_repeated_collisions" && c.schema != nil && c.schema.Implemented("ac-r6") == nil {
		return nil
	}
	switch which {
	case "equal_repeated_collisions", "dup_payload_parent_context", "trim_payload_links":
	default:
		return nil // these cases do not call build_ctx_in
	}
	tg, ok := c.p["target"].(map[string]any)
	if !ok {
		if which == "equal_repeated_collisions" {
			return requestError("equal_repeated_collisions needs target")
		}
		return requestError("any_direct %s needs target", which)
	}
	if err := requestFlags("context_options", "all_implemented ref_implemented no_yanglibrary enable_imp_features compile_obsolete leafref_extended leafref_linking builtin_plugins_only pattern_compat")(&requestCheck{p: tg}); err != nil {
		return err
	}
	targetSchema, _, _, verdict, err := c.r.buildContext(Request{ID: c.r.ID, BaseDir: c.r.BaseDir, Params: tg, buildContext: c.r.buildContext})
	if err != nil {
		return err
	}
	if verdict != "valid" {
		if which == "equal_repeated_collisions" {
			return requestError("equal_repeated_collisions context failed")
		}
		return requestError("any_direct target context failed")
	}
	if which == "equal_repeated_collisions" && c.schema != nil && c.schema.Implemented("ac-r6") != nil && (targetSchema == nil || targetSchema.Implemented("ac-r6") == nil) {
		return requestError("equal_repeated_collisions context failed")
	}
	return nil
}

func requestModule(c *requestCheck) error {
	name, present, err := strField(c.p, "module")
	if err != nil {
		return err
	}
	if present && c.schema.Implemented(name) == nil {
		return requestError("module %s is not implemented", name)
	}
	return nil
}

func requestTarget(c *requestCheck) error {
	v, exists := c.p["target"]
	if !exists {
		return nil
	}
	tg, ok := v.(map[string]any)
	if !ok {
		return requestError("diff target must be a context object")
	}
	if requestOperation(c) {
		return requestError("diff target with operation data")
	}
	if err := requestRead("data", false)(c); err != nil {
		return err
	}
	if !c.inputs["data"] {
		return requestError("diff target needs data")
	}
	for _, k := range slices.Sorted(maps.Keys(tg)) {
		if !seqContextKeys[k] {
			return requestError("unknown key %q in a diff target", k)
		}
	}
	return nil
}

func requestCompareTarget(c *requestCheck) error {
	v, exists := c.p["target"]
	if !exists {
		return nil
	}
	tg, ok := v.(map[string]any)
	if !ok {
		return requestError("compare target must be a context object")
	}
	for _, key := range slices.Sorted(maps.Keys(tg)) {
		if !seqContextKeys[key] {
			return requestError("unknown key %q in a compare target", key)
		}
	}
	return nil
}

func requestInsertTarget(c *requestCheck) error {
	v, exists := c.p["target"]
	if !exists {
		return nil
	}
	if !c.inputs["data"] {
		return requestError("insert target needs data")
	}
	tg, ok := v.(map[string]any)
	if !ok {
		return requestError("insert target must be a context object")
	}
	for _, key := range slices.Sorted(maps.Keys(tg)) {
		if !seqContextKeys[key] {
			return requestError("unknown key %q in an insert target", key)
		}
	}
	return nil
}
func requestMergeSelectors(c *requestCheck) error {
	if err := requestStrings("parent")(c); err != nil {
		return err
	}
	if c.p["parent"] != nil || c.p["detached_parent"] == true {
		if err := requestStrings("src_node")(c); err != nil {
			return err
		}
		if c.p["src_node"] == nil {
			return requestError("diff_merge parent needs src_node")
		}
	}
	if err := requestStrings("src_node")(c); err != nil {
		return err
	}
	if c.p["src_node"] != nil && c.p["module"] != nil {
		return requestError("diff_merge takes module or src_node, not both")
	}
	return nil
}

func checkRequest(r Request, s *yang.Schema, p map[string]any, exec executionContext, rows []requestRule) error {
	return (&requestCheck{r: r, p: p, schema: s, inputs: map[string]bool{}, exec: exec}).run(rows)
}

// requestChangeSelector permits direct XPath selection of nodes in incomplete lists.
func requestChangeSelector(c *requestCheck) error {
	_, node, err := strField(c.p, "node")
	if err != nil {
		return err
	}
	_, xpath, err := strField(c.p, "node_xpath")
	if err != nil {
		return err
	}
	// Native short-circuits the missing-selector case before reading value.
	if !node && !xpath {
		return requestError("change_term needs node and value")
	}
	_, value, err := strField(c.p, "value")
	if err != nil {
		return err
	}
	if !value {
		return requestError("change_term needs node and value")
	}
	if node && xpath {
		return requestError("change_term takes node or node_xpath, not both")
	}
	_, err = changeChildIndex(c.p)
	return err
}

// changeChildIndex selects an actual child by its one-based position, without
// requiring a complete keyed-list path or collapsing repeated leaf instances.
func changeChildIndex(p map[string]any) (int, error) {
	value, present := p["child"]
	if !present {
		return 0, nil
	}
	var index int
	switch v := value.(type) {
	case int:
		index = v
	case float64:
		if v > 0 && v <= 2147483647 && v == float64(int(v)) {
			index = int(v)
		}
	}
	if index <= 0 || index > 2147483647 {
		return 0, requestError("change_term child must be a positive integer")
	}
	return index, nil
}

func requestNoop(*requestCheck) error { return nil }

func requestRequired(message string, keys ...string) func(*requestCheck) error {
	return func(c *requestCheck) error {
		for _, k := range keys {
			_, present, err := strField(c.p, k)
			if err != nil {
				return err
			}
			if !present {
				return requestError("%s", message)
			}
		}
		return nil
	}
}
func requestKeys(p map[string]any, allowed string) error {
	keys := set(strings.Fields(allowed)...)
	for _, k := range slices.Sorted(maps.Keys(p)) {
		if !keys[k] {
			return requestError("unknown key %q", k)
		}
	}
	return nil
}
func requestDupTarget(c *requestCheck) error {
	v, exists := c.p["target"]
	if !exists {
		return nil
	}
	tg, ok := v.(map[string]any)
	if !ok || c.p["parent"] != nil {
		return requestError("dup target must be a context object, without a parent")
	}
	return requestKeys(tg, "searchdirs modules context_options")
}
func requestDupSelector(c *requestCheck) error {
	_, hasNode, err := strField(c.p, "node")
	if err != nil {
		return err
	}
	_, hasXPath, err := strField(c.p, "node_xpath")
	if err != nil {
		return err
	}
	if !hasNode && !hasXPath && c.p["detached"] != true {
		return requestError("dup step needs a node path")
	}
	if hasNode && hasXPath {
		return requestError("dup step needs exactly one of node and node_xpath")
	}
	return nil
}
func requestDupSource(c *requestCheck) error {
	v, exists := c.p["source"]
	if !exists {
		return nil
	}
	source, ok := v.(map[string]any)
	if !ok {
		return requestError("dup source needs data and parse_only true")
	}
	_, present, err := requestInput(c.r, source, "data")
	if err != nil {
		return err
	}
	if !present || source["parse_only"] != true {
		return requestError("dup source needs data and parse_only true")
	}
	if err := requestKeys(source, "format data_type data unknown parse_only rpc keep_input"); err != nil {
		return err
	}
	if c.p["target"] != nil {
		return requestError("dup source excludes target")
	}
	return nil
}
func requestVars(c *requestCheck) error {
	v, exists := c.p["vars"]
	if !exists {
		return nil
	}
	vars, ok := v.(map[string]any)
	if !ok {
		return requestError("trim vars must be an object")
	}
	for _, k := range slices.Sorted(maps.Keys(vars)) { // the harness sends sorted JSON keys
		if _, ok := vars[k].(string); !ok {
			return requestError("trim vars value of %s must be a string", k)
		}
	}
	return nil
}

var editKinds = []string{"merge_self", "set", "delete", "insert_term", "insert_inner", "insert_list", "insert_list2", "insert_opaq", "new_meta", "free_meta"}

func requestEditKeys(c *requestCheck) error {
	for _, k := range editKinds {
		if _, exists := c.p[k]; exists {
			allowed := set("do", k)
			for _, key := range slices.Sorted(maps.Keys(c.p)) {
				if !allowed[key] {
					return requestError("key %s not allowed in this edit step", key)
				}
			}
			return nil
		}
	}
	return requestKeys(c.p, "do merge merge_file merge_into format data_type unknown parse_options")
}

// requestEdit follows check_step's nested object checks, lyoracle.c:1780-1860.
// Merge invokes dparams_of in request preflight (lyoracle.c:1852-1853),
// and again during execution (2352). Module in the other edits is str_of,
// not a lookup in the request context.
func requestEdit(c *requestCheck) error {
	n, kind := 0, ""
	if c.inputs["merge"] {
		n++
	}
	if c.inputs["merge_into"] {
		n++
	}
	if c.p["delete"] != nil {
		n++
	}
	for _, k := range editKinds {
		if k == "delete" {
			continue
		}
		if _, exists := c.p[k]; exists {
			n++
			kind = k
		}
	}
	if n != 1 {
		return requestError("edit step needs exactly one of merge, merge_file, set, delete, insert_term, insert_inner, insert_list, insert_list2, insert_opaq, new_meta, free_meta")
	}
	if kind == "" {
		if c.inputs["merge"] || c.inputs["merge_into"] {
			return requestParams(c) // request phase: lyoracle.c:1853 check_step
		}
		return nil
	}
	if kind == "merge_self" {
		if c.p[kind] != true {
			return requestError("merge_self must be true")
		}
		return nil
	}
	o, ok := c.p[kind].(map[string]any)
	if !ok {
		switch kind {
		case "set":
			return requestError("set needs an object with path")
		case "new_meta", "free_meta":
			return requestError("new_meta/free_meta need an object with node and a module-qualified name")
		default:
			return requestError("insert_* need an object with name and at most one of parent, parent_opaq")
		}
	}
	sub := &requestCheck{r: c.r, p: o, schema: c.schema, inputs: map[string]bool{}, exec: c.exec}
	switch kind {
	case "new_meta", "free_meta":
		if err := requestRequired("new_meta/free_meta need an object with node and a module-qualified name", "node", "name")(sub); err != nil {
			return err
		}
		if !strings.Contains(str(o, "name", ""), ":") {
			return requestError("new_meta/free_meta need an object with node and a module-qualified name")
		}
		keys := "node name"
		if kind == "new_meta" {
			keys += " value"
		}
		if err := requestKeys(o, keys); err != nil {
			return err
		}
		if kind == "new_meta" {
			return requestRequired("new_meta needs a value", "value")(sub)
		}
	case "set":
		if err := requestRequired("set needs an object with path", "path")(sub); err != nil {
			return err
		}
		if err := requestKeys(o, "path value"); err != nil {
			return err
		}
		return requestStrings("value")(sub)
	default:
		return requestInsert(sub, kind)
	}
	return nil
}
func requestInsert(c *requestCheck, kind string) error {
	o := c.p
	if err := requestStrings("parent", "parent_opaq")(c); err != nil {
		return err
	}
	if kind != "insert_opaq" && kind != "insert_list2" {
		if err := requestBools("detached_parent")(c); err != nil {
			return err
		}
	}
	if err := requestRequired("insert_* need an object with name and at most one of parent, parent_opaq", "name")(c); err != nil {
		return err
	}
	if (o["parent"] != nil && o["parent_opaq"] != nil) ||
		kind != "insert_list2" && kind != "insert_opaq" && o["detached_parent"] == true &&
			(o["parent"] != nil || o["parent_opaq"] != nil) {
		return requestError("insert_* need an object with name and at most one of parent, parent_opaq")
	}
	keys := map[string]string{
		"insert_term":  "module parent parent_opaq detached_parent name value options",
		"insert_inner": "module parent parent_opaq detached_parent name options",
		"insert_opaq":  "parent parent_opaq name value prefix module xml",
		"insert_list":  "module parent parent_opaq detached_parent name keys options",
		"insert_list2": "module parent parent_opaq name keys options",
	}
	if err := requestKeys(o, keys[kind]); err != nil {
		return err
	}
	if kind == "insert_opaq" {
		if err := requestStrings("value", "prefix")(c); err != nil {
			return err
		}
		if err := requestRequired("insert_opaq needs a module", "module")(c); err != nil {
			return err
		}
		return requestBools("xml")(c)
	}
	if err := requestStrings("module")(c); err != nil {
		return err
	}
	if o["module"] == nil && o["parent"] == nil && o["parent_opaq"] == nil && o["detached_parent"] != true {
		return requestError("insert_term/inner/list/list2 need a module or a parent")
	}
	flags := "output store_only canon"
	if kind == "insert_inner" {
		flags = "output"
	}
	if err := requestFlags("options", flags)(c); err != nil {
		return err
	}
	switch kind {
	case "insert_term":
		return requestRequired("insert_term needs a value", "value")(c)
	case "insert_list2":
		return requestStrings("keys")(c)
	case "insert_list":
		if v, exists := o["keys"]; exists {
			keys, ok := v.([]any)
			if !ok || len(keys) > 16 {
				return requestError("insert_list keys must be an array of at most 16 strings or nulls")
			}
			for _, k := range keys {
				if k != nil {
					if _, ok := k.(string); !ok {
						return requestError("insert_list keys must be an array of at most 16 strings or nulls")
					}
				}
			}
		}
	}
	return nil
}

// Step input errors carry their step name; unrelated root keys cannot affect
// op_data's missing-input error.
func requestStepData(c *requestCheck) error {
	if err := requestRead("data", false)(c); err != nil {
		return err
	}
	if !c.inputs["data"] {
		return requestError("%s step needs data", c.exec.name())
	}
	return nil
}

// requestCompareInput accepts the ordinary parsed second tree or a single opaque
// root constructed by the oracle. The latter reaches values an XML parser must
// reject, and remains unsupported by the Go engine until opaque constructors are public.
func requestCompareInput(c *requestCheck) error {
	if err := requestRead("data", false)(c); err != nil {
		return err
	}
	io, hasOpaq := c.p["insert_opaq"]
	if !c.inputs["data"] && !hasOpaq {
		return requestError("compare step needs data")
	}
	if c.inputs["data"] && hasOpaq {
		return requestError("compare step needs exactly one of data, insert_opaq")
	}
	if !hasOpaq {
		return nil
	}
	o, ok := io.(map[string]any)
	if !ok {
		return requestError("compare insert_opaq needs an object with name and module")
	}
	// lyoracle.c:3325-3328 checks name/module before the key list.
	sub := &requestCheck{r: c.r, p: o, schema: c.schema, inputs: map[string]bool{}, exec: c.exec}
	if err := requestRequired("compare insert_opaq needs an object with name and module", "name", "module")(sub); err != nil {
		return err
	}
	if err := requestKeys(o, "name value prefix module xml"); err != nil {
		return err
	}
	if err := requestStrings("value", "prefix")(sub); err != nil {
		return err
	}
	return requestBools("xml")(sub)
}

// checkOpInit follows the local initializers after build_ctx/data_prelude,
// before the caller checks whether that prelude returned a usable context.
func checkOpInit(r Request, s *yang.Schema, op string) error {
	switch op {
	case "data":
		return checkRequest(r, s, r.Params, execData, dataInitRules)
	case "xpath":
		return requestStrings("xpath", "context_path", "cur_module", "xpath_format")(&requestCheck{p: r.Params})
	case "atoms":
		if err := requestStrings("xpath", "path", "context_path")(&requestCheck{p: r.Params}); err != nil {
			return err
		}
		return requestFlags("atom_options", "schema output no_match_error")(&requestCheck{p: r.Params})
	}
	return nil
}
