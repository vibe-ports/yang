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
// "do" are ignored there (lyoracle.c:2692-2714), while check_step owns "do" for sequence steps
// (lyoracle.c:1700-1722, 2517-2549).
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
	execStepLink
	execStepLinks
)

var rootExecutionContexts = map[string]executionContext{
	"schema": execSchema, "data": execData, "xpath": execXPath, "diff": execDiff,
	"sequence": execSequence, "atoms": execAtoms,
}

var stepExecutionContexts = map[string]executionContext{
	"parse": execStepParse, "validate": execStepValidate, "edit": execStepEdit,
	"change_term": execStepChangeTerm, "dump": execStepDump, "dup": execStepDup,
	"insert": execStepInsert, "compare": execStepCompare, "diff": execStepDiff,
	"diff_parse": execStepDiffParse, "diff_merge": execStepDiffMerge,
	"diff_apply": execStepDiffApply, "diff_reverse": execStepDiffReverse,
	"trim": execStepTrim, "link": execStepLink, "links": execStepLinks,
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
						return requestError("diff single and merge must be booleans")
					case execStepDup, execStepChangeTerm:
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

// requestInput is input_of (lyoracle.c:257-268), including str_of's null
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
	case "rpc", "reply", "notif", "notif-netconf", "notif-restconf":
		return true
	}
	return false
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
	{1223, requestEnum("data_type", "data-operational", "config data-operational data get getconfig edit rpc reply notif notif-netconf notif-restconf")}, // caller phase: request preflight or execution; lyoracle.c:1223-1245
	{1250, func(c *requestCheck) error { // caller phase: request preflight or execution; lyoracle.c:1250-1252
		t := str(c.p, "data_type", "")
		if (t == "notif-netconf" || t == "notif-restconf") && c.p["parse_only"] != true {
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
	{1442, requestFlags("print_options", "empty_leaf_list")},                                         // request prelude; lyoracle.c:1442 flags_of
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
	{1281, requestParent}, // execution phase; lyoracle.c:1653 calls parse_one with NULL rpc_src
}

var sequenceRequestRules = []requestRule{
	{2509, func(c *requestCheck) error { // request phase; lyoracle.c:2509
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
	{1706, func(c *requestCheck) error { // request phase; lyoracle.c:1706-1722 keys_only, unknown kinds allow only do
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
		{1725, requestEditKeys},             // request phase; lyoracle.c:1725-1735
		{1764, requestRead("merge", false)}, // request phase; lyoracle.c:1764
		{1764, requestStrings("delete")},    // request phase; lyoracle.c:1764
		{1774, requestEdit},                 // request phase; lyoracle.c:1774-1851 nested edit objects
	},
	"change_term": {
		{1857, requestRequired("change_term needs node and value", "node", "value")}, // request phase; lyoracle.c:1857
		{1860, requestBools("canon")}, // request phase; lyoracle.c:1860
	},
	"dump": {
		{1864, requestStrings("with_defaults")},                                                          // request phase; lyoracle.c:1864 wd_of
		{1864, requestEnum("with_defaults", "explicit", "explicit trim all all-tagged implicit-tagged")}, // request phase; lyoracle.c:1864
	},
	"dup": {
		{1868, requestRequired("dup step needs a node path", "node")},                        // request phase; lyoracle.c:1868
		{1871, requestStrings("parent")},                                                     // request phase; lyoracle.c:1871
		{1872, requestFlags("options", "recursive no_meta with_parents with_flags no_lyds")}, // request phase; lyoracle.c:1872
		{1873, requestBools("siblings")},                                                     // request phase; lyoracle.c:1873
		{1876, requestDupTarget},                                                             // request phase; lyoracle.c:1876-1882
	},
	"trim": {
		{1967, requestRequired("trim step needs an xpath", "xpath")}, // request phase; lyoracle.c:1967
		{1970, requestVars}, // request phase; lyoracle.c:1970-1976
	},
	"link":         {{1978, requestNoop}}, // request phase; lyoracle.c:1978 no fields beyond keys_only
	"links":        {{1978, requestNoop}}, // request phase; lyoracle.c:1978
	"diff_reverse": {{1978, requestNoop}}, // request phase; lyoracle.c:1978

	"parse": {
		{1738, requestParams},             // request phase; lyoracle.c:1738
		{1739, requestParseOnly},          // request phase; lyoracle.c:1739
		{1743, requestRead("rpc", false)}, // request phase; lyoracle.c:1743
		{1743, requestReply},              // request phase; lyoracle.c:1743
		{1746, requestStepData},           // request phase; lyoracle.c:1746
	},
	"validate": {
		{1751, requestStrings("module")},   // request phase; lyoracle.c:1751
		{1753, requestParams},              // request phase; lyoracle.c:1753
		{1754, requestBools("final_only")}, // request phase; lyoracle.c:1754 null is NOT a bool
		{1757, func(c *requestCheck) error { // request phase; lyoracle.c:1757
			if c.p["final_only"] == true && c.p["module"] == nil {
				return requestError("final_only needs module")
			}
			return nil
		}},
		{1760, func(c *requestCheck) error { // request phase; lyoracle.c:1760 original context, before any step executes
			if c.p["module"] != nil && c.schema.Implemented(str(c.p, "module", "")) == nil {
				return requestError("validate module %s not implemented", c.p["module"])
			}
			return nil
		}},
	},
	"insert": {
		{1885, requestStrings("node", "parent")}, // request phase; lyoracle.c:1885-1886
		{1887, requestRead("data", false)},       // request phase; lyoracle.c:1887
		{1888, func(c *requestCheck) error { // request phase; lyoracle.c:1888-1893
			if c.inputs["data"] {
				if err := requestParams(c); err != nil {
					return err
				}
				return requestDatastore(c)
			}
			if c.p["node"] == nil {
				return requestError("insert step needs a node or data")
			}
			return nil
		}},
	},
	"compare": {
		{1896, requestParams},                                           // request phase; lyoracle.c:1896
		{1897, requestDatastore},                                        // request phase; lyoracle.c:1897
		{1900, requestStepData},                                         // request phase; lyoracle.c:1900
		{1903, requestStrings("first", "second")},                       // request phase; lyoracle.c:1903-1904
		{1905, requestFlags("options", "full_recursion defaults opaq")}, // request phase; lyoracle.c:1905
	},
	"diff": {
		{1909, requestParams},             // request phase; lyoracle.c:1909
		{1919, requestRead("rpc", false)}, // request phase; lyoracle.c:1919 BEFORE the parse_only check
		{1921, requestParseOnly},          // request phase; lyoracle.c:1921
		{1925, requestReply},              // request phase; lyoracle.c:1925
		{1928, func(c *requestCheck) error { // request phase; lyoracle.c:1928 presence, not IsTrue
			if _, exists := c.p["keep_input"]; exists && !c.inputs["rpc"] {
				return requestError("diff step: keep_input only with rpc")
			}
			return nil
		}},
		{1932, requestFlags("options", "defaults meta")},  // request phase; lyoracle.c:1932
		{1933, requestFlags("merge_options", "defaults")}, // request phase; lyoracle.c:1933
		{1934, requestStrings("node", "data_node")},       // request phase; lyoracle.c:1934-1935
		{1936, requestTarget},                             // request phase; lyoracle.c:1936-1946
		{1948, requestBools("single", "merge")},           // request phase; lyoracle.c:1948-1950
	},
	"diff_parse": {
		{1909, requestParams},    // request phase; lyoracle.c:1909
		{1910, requestDatastore}, // request phase; lyoracle.c:1910
		{1913, requestStepData},  // request phase; lyoracle.c:1913
	},
	"diff_merge": {
		{1909, requestParams},                       // request phase; lyoracle.c:1909
		{1910, requestDatastore},                    // request phase; lyoracle.c:1910
		{1913, requestStepData},                     // request phase; lyoracle.c:1913
		{1953, requestFlags("options", "defaults")}, // request phase; lyoracle.c:1953
		{1954, requestStrings("module")},            // request phase; lyoracle.c:1954
		{1955, requestMergeSelectors},               // request phase; lyoracle.c:1955-1959
	},
	"diff_apply": {{1963, requestStrings("module")}}, // request phase; lyoracle.c:1963
}

// These checks happen only when a step executes. In particular, diff.data is
// not read by check_step unless target is present, and module_of uses the
// retained tree's context, unlike validate's preflight lookup.
var stepExecutionRules = map[string][]requestRule{
	"diff":       {{2355, requestRead("data", false)}}, // execution phase; lyoracle.c:2355 step_diff
	"diff_merge": {{2438, requestModule}},              // execution phase; lyoracle.c:2438 module_of
	"diff_apply": {{2570, requestModule}},              // execution phase; lyoracle.c:2570 module_of
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
func requestMergeSelectors(c *requestCheck) error {
	if err := requestStrings("parent")(c); err != nil {
		return err
	}
	if c.p["parent"] != nil {
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

var editKinds = []string{"set", "delete", "insert_term", "insert_inner", "insert_list", "insert_list2", "insert_opaq", "new_meta", "free_meta"}

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
	return requestKeys(c.p, "do merge merge_file format data_type unknown parse_options")
}

// requestEdit follows check_step's nested object checks, lyoracle.c:1774-1851.
// Merge invokes dparams_of in request preflight (lyoracle.c:1843-1844),
// and again during execution (2285). Module in the other edits is str_of,
// not a lookup in the request context.
func requestEdit(c *requestCheck) error {
	n, kind := 0, ""
	if c.inputs["merge"] {
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
		if c.inputs["merge"] {
			return requestParams(c) // request phase: lyoracle.c:1844 check_step
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
	if err := requestRequired("insert_* need an object with name and at most one of parent, parent_opaq", "name")(c); err != nil {
		return err
	}
	if o["parent"] != nil && o["parent_opaq"] != nil {
		return requestError("insert_* need an object with name and at most one of parent, parent_opaq")
	}
	keys := map[string]string{
		"insert_term":  "module parent parent_opaq name value options",
		"insert_inner": "module parent parent_opaq name options",
		"insert_opaq":  "parent parent_opaq name value prefix module xml",
		"insert_list":  "module parent parent_opaq name keys options",
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
	if o["module"] == nil && o["parent"] == nil && o["parent_opaq"] == nil {
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
