// SPDX-License-Identifier: BSD-3-Clause

//go:build oracle

package data

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

const (
	nativeStdoutBudget  = 64 << 20
	nativeStderrBudget  = 1 << 20
	nativeRecordBudget  = 8 << 20
	nativeRecordsBudget = 32768
)

var errNativeProjectionBudget = errors.New("native projection input byte budget exceeded")

// Charge the exact transport representation before allocating a full copy.
// Raw rows remain JSON rather than undergoing Marshal's HTML escaping; valid
// JSON is required here so a candidate row cannot alter the transport envelope.
func nativeEvidencePayload(script string, rows []json.RawMessage, project bool, readContext *bool, limit int) ([]byte, error) {
	const start = `{"script_hex":"`
	const middle = `","rows":[`
	const projectKey = `],"project":`
	const contextKey = `,"read_context":`
	projectText := "false"
	if project {
		projectText = "true"
	}
	contextText := "false"
	if readContext != nil && *readContext {
		contextText = "true"
	}
	size := len(start) + len(middle) + len(projectKey) + len(projectText) + 1
	if readContext != nil {
		size += len(contextKey) + len(contextText)
	}
	if size > limit || len(script) > (limit-size)/2 {
		return nil, errNativeProjectionBudget
	}
	size += 2 * len(script)
	if len(rows) > 0 {
		if len(rows)-1 > limit-size {
			return nil, errNativeProjectionBudget
		}
		size += len(rows) - 1
	}
	for _, row := range rows {
		rowBytes := len(row)
		if row == nil {
			rowBytes = 4
		} // RawMessage(nil) represents null.
		if rowBytes > limit-size {
			return nil, errNativeProjectionBudget
		}
		size += rowBytes
	}
	for _, row := range rows {
		trimmed := bytes.TrimSpace(row)
		if !utf8.Valid(row) || !json.Valid(row) || len(trimmed) == 0 || trimmed[0] != '{' || trimmed[len(trimmed)-1] != '}' {
			return nil, fmt.Errorf("invalid JSON object in native projection row")
		}
	}
	payload := make([]byte, 0, size)
	payload = append(payload, start...)
	const digits = "0123456789abcdef"
	for i := range len(script) {
		payload = append(payload, digits[script[i]>>4], digits[script[i]&15])
	}
	payload = append(payload, middle...)
	for i, row := range rows {
		if i > 0 {
			payload = append(payload, ',')
		}
		if row == nil {
			payload = append(payload, "null"...)
		} else {
			payload = append(payload, row...)
		}
	}
	payload = append(payload, projectKey...)
	payload = append(payload, projectText...)
	if readContext != nil {
		payload = append(payload, contextKey...)
		payload = append(payload, contextText...)
	}
	payload = append(payload, '}')
	return payload, nil
}

type nativeCapture struct {
	buffer                      bytes.Buffer
	limit, recordBytes, records int
	framing                     bool
	kill                        func() error
	failure                     error
}

func (w *nativeCapture) Len() int      { return w.buffer.Len() }
func (w *nativeCapture) Bytes() []byte { return w.buffer.Bytes() }

func (w *nativeCapture) Write(p []byte) (int, error) {
	if w.failure != nil {
		return 0, w.failure
	}
	fail := func(reason string) (int, error) {
		w.failure = fmt.Errorf("native transport %s budget exceeded", reason)
		// The process can exit concurrently; Wait below still checks its result.
		if err := w.kill(); err != nil {
			w.failure = fmt.Errorf("%w (terminate producer: %v)", w.failure, err)
		}
		return 0, w.failure
	}
	if len(p) > w.limit-w.buffer.Len() {
		return fail("byte")
	}
	if w.framing {
		for _, part := range bytes.SplitAfter(p, []byte{'\n'}) {
			size := len(part)
			ended := size > 0 && part[size-1] == '\n'
			if ended {
				size--
			}
			w.recordBytes += size
			if w.recordBytes > nativeRecordBudget {
				return fail("record byte")
			}
			if ended {
				w.records++
				w.recordBytes = 0
				if w.records > nativeRecordsBudget {
					return fail("record count")
				}
			}
		}
	}
	return w.buffer.Write(p)
}

// Every native and projection subprocess uses this capture before decoding. The
// pipes remain separate so diagnostics cannot masquerade as evidence records.
func nativeCommandOutput(cmd *exec.Cmd) ([]byte, error) {
	if cmd.WaitDelay == 0 {
		cmd.WaitDelay = time.Second
	}
	kill := func() error { return cmd.Process.Kill() }
	stdout := &nativeCapture{limit: nativeStdoutBudget, framing: true, kill: kill}
	stderr := &nativeCapture{limit: nativeStderrBudget, kill: kill}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	err := cmd.Run()
	if stdout.failure != nil {
		return stdout.Bytes(), stdout.failure
	}
	if stderr.failure != nil {
		return stdout.Bytes(), stderr.failure
	}
	if err != nil {
		// Preserve ExitError for admission controls that assert the exit status.
		if exit, ok := err.(*exec.ExitError); ok {
			exit.Stderr = bytes.Clone(stderr.Bytes())
		}
		return stdout.Bytes(), fmt.Errorf("%w: %s", err, stderr.Bytes())
	}
	return stdout.Bytes(), nil
}

func nativeTranscriptRows(output []byte) ([]json.RawMessage, error) {
	if len(output) > nativeStdoutBudget {
		return nil, fmt.Errorf("native stdout byte budget exceeded")
	}
	if !utf8.Valid(output) {
		return nil, fmt.Errorf("invalid UTF-8 in native transcript")
	}
	if len(output) == 0 || output[len(output)-1] != '\n' {
		return nil, fmt.Errorf("missing native JSONL terminator")
	}
	if len(output) > nativeStdoutBudget {
		return nil, fmt.Errorf("native stdout byte budget exceeded")
	}
	lines := bytes.Split(output[:len(output)-1], []byte{'\n'})
	if len(lines) > nativeRecordsBudget {
		return nil, fmt.Errorf("native record count budget exceeded")
	}
	rows := make([]json.RawMessage, 0, len(lines))
	for _, line := range lines {
		if len(line) == 0 || len(line) > nativeRecordBudget || bytes.ContainsAny(line, "\r\x00") {
			return nil, fmt.Errorf("invalid native JSONL record framing or size")
		}
		var row nativeDriverResult
		if err := json.Unmarshal(line, &row); err != nil {
			return nil, err
		}
		if row.raw == nil {
			return nil, fmt.Errorf("native JSONL record must be an object")
		}
		rows = append(rows, bytes.Clone(line))
	}
	return rows, nil
}

// Admission controls validate the completed prefix against its own requests,
// then compare every record including primitive events and reuse notices.
func nativeDecodePrefix(script string, output []byte) ([]json.RawMessage, error) {
	rows, err := nativeTranscriptRows(output)
	if err != nil {
		return nil, err
	}
	readContext := true // Admission runners independently select --read-schema.
	if _, err := nativeIdentityEvidenceRunContext(script, rows, false, &readContext); err != nil {
		return nil, err
	}
	var completed []nativeDriverResult
	for _, line := range rows {
		var row nativeDriverResult
		if err := json.Unmarshal(line, &row); err != nil {
			return nil, err
		}
		if row.Event != "driver.ready.actual-children-callback" && row.Event != "allocation.address-reuse" {
			completed = append(completed, row)
		}
	}
	commands := 0
	for _, line := range strings.Split(script, "\n") {
		fields := strings.Fields(line)
		if len(fields) > 0 && !strings.HasPrefix(fields[0], "#") {
			commands++
		}
	}
	if commands == 0 {
		if len(completed) != 0 {
			return nil, fmt.Errorf("unexpected evidence in empty native prefix")
		}
		return rows, nil
	}
	if err := nativeCompletedReplay(script, completed, ""); err != nil {
		return nil, err
	}
	return rows, nil
}

func TestNativeTransportBudgets(t *testing.T) {
	for _, tc := range []struct {
		name  string
		data  []byte
		limit int
		frame bool
	}{
		{"stdout", []byte("abcd"), 3, true},
		{"stderr", []byte("abcd"), 3, false},
		{"record", bytes.Repeat([]byte{'x'}, nativeRecordBudget+1), nativeStdoutBudget, true},
		{"records", bytes.Repeat([]byte{'\n'}, nativeRecordsBudget+1), nativeStdoutBudget, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			killed := 0
			capture := &nativeCapture{limit: tc.limit, framing: tc.frame, kill: func() error { killed++; return nil }}
			if _, err := capture.Write(tc.data); err == nil {
				t.Fatal("unbounded capture accepted")
			}
			if killed != 1 || capture.Len() != 0 {
				t.Fatalf("capture retained %d bytes, kills %d", capture.Len(), killed)
			}
		})
	}
}

func TestNativeTranscriptStrictEncoding(t *testing.T) {
	for _, output := range [][]byte{
		[]byte("{\"event\":\"\xff\"}\n"), []byte("{\"event\":\"\xfe\"}\n"),
		[]byte("\xef\xbb\xbf{}\n"), []byte("{}"), []byte("{}\r\n"), []byte("{}\n\n"),
		[]byte("n\x00u\x00l\x00l\x00\n"),
	} {
		if _, err := nativeTranscriptRows(output); err == nil {
			t.Fatalf("accepted %q", output)
		}
		if _, err := nativeDecodePrefix("", output); err == nil {
			t.Fatalf("prefix accepted %q", output)
		}
	}
}

// These controls run real processes, including a producer that formerly forced
// CombinedOutput to retain its entire 64 MiB line before scanner rejection.
func TestNativeTransportProcesses(t *testing.T) {
	for _, tc := range []struct {
		name, program string
		max           int
	}{
		{"stdout-record", "import os; os.write(1,b'x'*(64<<20))", nativeRecordBudget},
		{"stderr", "import os; os.write(2,b'x'*(4<<20))", 0},
		{"inherited-pipes", "import os,time; child=os.fork(); (time.sleep(2),os._exit(0)) if child==0 else os.write(1,b'x'*(16<<20))", nativeRecordBudget},
		{"record-count", "import os; os.write(1,b'{}\\n'*65536)", nativeStdoutBudget},
	} {
		t.Run(tc.name, func(t *testing.T) {
			//nolint:gosec // Fixed synthetic producer controls exercise capture limits.
			cmd := exec.Command("python3", "-c", tc.program)
			output, err := nativeCommandOutput(cmd)
			if err == nil || !bytes.Contains([]byte(err.Error()), []byte("budget exceeded")) {
				t.Fatalf("producer escaped capture budget: %v (retained=%d)", err, len(output))
			}
			if len(output) > tc.max {
				t.Fatalf("retained %d bytes, budget %d", len(output), tc.max)
			}
			t.Logf("capture work retained=%d limit=%d", len(output), tc.max)
		})
	}
}

func TestNativePythonTransport(t *testing.T) {
	//nolint:gosec // Fixed repository-owned transport controls.
	cmd := exec.Command("python3", "../conformance/oracle/native_registration/transport_test.py")
	if output, err := nativeCommandOutput(cmd); err != nil {
		t.Fatalf("Python transport controls: %v\n%s", err, output)
	}
}

func TestNativeProjectionPayloadBudget(t *testing.T) {
	readContext := true
	rows := []json.RawMessage{json.RawMessage(`{"html":"<>&","value":"x"}`), json.RawMessage(`{}`)}
	payload, err := nativeEvidencePayload("a\xff", rows, false, &readContext, 256)
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(payload) || !bytes.Contains(payload, []byte(`"script_hex":"61ff"`)) || !bytes.Contains(payload, []byte(`<>&`)) {
		t.Fatalf("payload changed request/row bytes: %s", payload)
	}
	boundary := len(payload)
	if _, err := nativeEvidencePayload("a\xff", rows, false, &readContext, boundary); err != nil {
		t.Fatal(err)
	}
	if _, err := nativeEvidencePayload("a\xff", rows, false, &readContext, boundary-1); !errors.Is(err, errNativeProjectionBudget) {
		t.Fatalf("accepted oversized representation: %v", err)
	}

	if _, err := nativeEvidencePayload("", []json.RawMessage{json.RawMessage(`{}],"project":true,"rows":[{}`)}, false, nil, 256); err == nil {
		t.Fatal("row escaped projection envelope")
	}
}

func TestNativeProjectionPayloadRejectAllocations(t *testing.T) {
	largeScript := strings.Repeat("x", 4096)
	largeRows := []json.RawMessage{json.RawMessage(`{"html":"` + strings.Repeat("<>&", 4096) + `"}`)}
	for _, input := range []struct {
		name, script string
		rows         []json.RawMessage
	}{
		{"script-hex", largeScript, nil}, {"raw-rows", "", largeRows},
	} {
		t.Run(input.name, func(t *testing.T) {
			allocations := testing.AllocsPerRun(5, func() {
				result, err := nativeEvidencePayload(input.script, input.rows, true, nil, 64)
				if result != nil || !errors.Is(err, errNativeProjectionBudget) {
					t.Fatal("missing budget rejection")
				}
			})
			if allocations != 0 {
				t.Fatalf("rejected payload allocated full copies: %.0f allocations", allocations)
			}
			t.Logf("rejected payload allocations=%.0f", allocations)
		})
	}
}
