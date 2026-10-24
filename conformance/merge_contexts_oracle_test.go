// SPDX-License-Identifier: BSD-3-Clause

//go:build oracle

package conformance

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
)

// nativeCrashSignal runs the native oracle on p and returns the signal that ended it: SIGSEGV,
// SIGABRT, or SIGTRAP (macOS libmalloc traps the heap corruption that glibc aborts on), also
// through a wrapper shell that reports them as exit status 128+signal. Any other outcome (a
// response, a timeout, another exit or signal) is an error.
func nativeCrashSignal(t *testing.T, base string, p map[string]any) (string, error) {
	t.Helper()
	oracle, err := filepath.Abs(OracleBinary("oracle"))
	if err != nil {
		return "", err
	}
	b, err := json.Marshal(p)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), nativeRequestTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, oracle) //nolint:gosec // repo's own oracle binary
	cmd.Dir = base
	cmd.Stdin = bytes.NewReader(b)
	out, err := cmd.Output()
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		return "", errors.New("native oracle did not crash: " + string(out))
	}
	if ws, ok := exit.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		if name, ok := crashSignals[ws.Signal()]; ok {
			return name, nil
		}
		return "", errors.New("native oracle ended by " + ws.Signal().String())
	}
	for sig, name := range crashSignals {
		if exit.ExitCode() == 128+int(sig) {
			return name, nil
		}
	}
	return "", errors.New("native oracle exited " + exit.String())
}

var crashSignals = map[syscall.Signal]string{syscall.SIGSEGV: "SIGSEGV", syscall.SIGABRT: "SIGABRT", syscall.SIGTRAP: "SIGTRAP"}
