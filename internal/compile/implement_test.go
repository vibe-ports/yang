// SPDX-License-Identifier: BSD-3-Clause

package compile

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func newCtx(t *testing.T, opts Options, dir string) *Context {
	t.Helper()
	c, _, err := NewContext(opts, os.DirFS(dir))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// TestAugmentImplementsTarget: the target module of a top-level augment
// becomes implemented and records the augmenting module (lys_implement).
func TestAugmentImplementsTarget(t *testing.T) {
	c := newCtx(t, Options{}, filepath.Join(corpus, "load", "implement"))
	ag, _, err := c.Load("ag", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	tg := c.implemented("tg")
	if tg == nil || len(tg.augmentedBy) != 1 || tg.augmentedBy[0] != ag || !tg.compiled || tg.toCompile {
		t.Fatalf("tg %+v", tg)
	}
}

// TestDeviationUnsupported: implementing a module with deviations fails
// with ErrUnsupported (U-0020) and the load is reverted; the target stays
// import-only.
func TestDeviationUnsupported(t *testing.T) {
	c := newCtx(t, Options{}, filepath.Join(corpus, "load", "implement"))
	n := len(c.Modules)
	if _, _, err := c.Load("dv", "", nil); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("got %v", err)
	}
	if len(c.Modules) != n {
		t.Errorf("%d modules after the failed load, want %d", len(c.Modules), n)
	}
	if _, _, err := c.Load("dvu", "", nil); err != nil { // import-only is fine
		t.Fatal(err)
	}
}

// TestUnsupportedExtensionPlugin: an instance of an unported plugin fails
// the load (U-0023), a definition alone does not.
func TestUnsupportedExtensionPlugin(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "ietf-restconf@2017-01-26.yang", `module ietf-restconf { namespace "urn:ietf:params:xml:ns:yang:ietf-restconf";
  prefix rc; revision 2017-01-26; extension yang-data { argument name; } }`)
	write(t, dir, "yd.yang", `module yd { namespace urn:yd; prefix yd; import ietf-restconf { prefix rc; } rc:yang-data d; }`)
	c := newCtx(t, Options{}, dir)
	if _, _, err := c.Load("ietf-restconf", "", nil); err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.Load("yd", "", nil); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("got %v", err)
	}
}

// TestRevertUnlinksDerived: identities of a module removed by a failed load
// leave the derived lists of the remaining modules.
func TestRevertUnlinksDerived(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "base.yang", `module base { namespace urn:base; prefix b; identity root; }`)
	write(t, dir, "bad.yang", `module bad { namespace urn:bad; prefix x; import base { prefix b; }
  identity d { base b:root; } identity e { base nope; } }`)
	c := newCtx(t, Options{}, dir)
	base, _, err := c.Load("base", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.Load("bad", "", nil); err == nil {
		t.Fatal("bad accepted")
	}
	if d := base.Schema.Identity("root").Derived; len(d) != 0 {
		t.Errorf("root still derived by %v", d[0].Name)
	}
}

// TestDepSetRestart: LY_ERECOMPILE from unres compiles the whole dep set
// again (lys_compile_depset_r); an unres error fails and reverts the load.
func TestDepSetRestart(t *testing.T) {
	runs := func(restart bool) int {
		c := newCtx(t, Options{}, filepath.Join(corpus, "load", "features"))
		calls := 0
		c.unresHook = func(*Context) error { // one run per dep set
			if calls++; restart && calls == 1 {
				return errRecompile
			}
			return nil
		}
		m, _, err := c.Load("fa", "", nil)
		if err != nil || !m.compiled || m.toCompile {
			t.Fatalf("err %v, %+v", err, m)
		}
		return calls
	}
	if a, b := runs(false), runs(true); b != a+1 {
		t.Errorf("unres runs %d, with a restart %d", a, b)
	}
	c := newCtx(t, Options{}, filepath.Join(corpus, "load", "features"))
	boom := errors.New("boom")
	c.unresHook = func(*Context) error { return boom }
	if _, _, err := c.Load("fb", "", nil); !errors.Is(err, boom) || c.implemented("fb") != nil || c.latest("fb") != nil {
		t.Fatalf("got %v", err)
	}
}

// TestDepSets: modules without features and anything to compile get their
// own sets; a module with features is grouped with its importers.
func TestDepSets(t *testing.T) {
	c := newCtx(t, Options{}, filepath.Join(corpus, "load", "features"))
	if _, _, err := c.Load("fc", "", nil); err != nil {
		t.Fatal(err)
	}
	for _, set := range c.depSets() {
		var names []string
		for _, m := range set {
			names = append(names, m.Name)
		}
		if len(set) > 1 && js(names) != `["fa","fc"]` {
			t.Errorf("set %v", names)
		}
	}
}

// TestImportFeatures: with AllImplemented and EnableImportFeatures the
// imported module is implemented with all its features (LY_CTX_ENABLE_IMP_FEATURES).
func TestImportFeatures(t *testing.T) {
	c := newCtx(t, Options{AllImplemented: true, EnableImportFeatures: true}, filepath.Join(corpus, "load", "features"))
	if _, diags, err := c.Load("fc", "", []string{"*"}); err != nil {
		t.Fatal(err, diags)
	}
	for _, f := range c.implemented("fa").features {
		if !f.enabled {
			t.Errorf("fa feature %s disabled", f.p.Name)
		}
	}
}
