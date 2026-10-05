// SPDX-License-Identifier: BSD-3-Clause

package conformance

import (
	"fmt"
	"path/filepath"
	"runtime"
)

// CanonicalArch is the uname -m of the oracle that may write goldens (x87 long double, see
// oracle/README.md).
const CanonicalArch = "x86_64"

// unameArch maps GOARCH to `uname -m` (what lyoracle reports in "arch").
func unameArch(goarch string) string {
	switch goarch {
	case "amd64":
		return "x86_64"
	case "arm64":
		return "aarch64"
	}
	return goarch
}

// OracleBinary is the lyoracle built for this process's architecture (the Makefile builds
// oracle/lyoracle-$(uname -m), so containers of different platforms never share a binary).
func OracleBinary(oracleDir string) string {
	return filepath.Join(oracleDir, "lyoracle-"+unameArch(runtime.GOARCH))
}

// CheckOracleArch fails when the oracle that produced r runs on another architecture than this
// process (a foreign or stale binary), or, with write, is not the canonical one.
func CheckOracleArch(r Response, write bool) error {
	got, _ := r["arch"].(string)
	if got == "" {
		return fmt.Errorf("oracle reports no \"arch\": stale lyoracle, rebuild with make oracle")
	}
	return checkArch(got, runtime.GOARCH, write)
}

func checkArch(got, goarch string, write bool) error {
	if want := unameArch(goarch); got != want {
		return fmt.Errorf("oracle arch %s != Go process arch %s: foreign or stale lyoracle, rebuild with make oracle", got, want)
	}
	if write && got != CanonicalArch {
		return fmt.Errorf("refusing to write goldens from a %s oracle: use DEV_PLATFORM=linux/amd64 ./dev make oracle-golden", got)
	}
	return nil
}
