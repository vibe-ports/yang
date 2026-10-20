// SPDX-License-Identifier: BSD-3-Clause

package conformance

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
)

// The digest is over the exact JSON sent to the native process, not a selected
// subset or a normalized request. Responses include all native fields. Transport
// failures are recorded too and remain failures on replay.
type requestCacheEntry struct {
	SHA256   string          `json:"sha256"`
	Request  json.RawMessage `json:"request"`
	Response Response        `json:"response"`
	Error    string          `json:"error,omitempty"`
}

type requestCacheLocation struct {
	offset int64
	length int64
}

type requestOracleRunner struct {
	native    func(string, map[string]any) (Response, error)
	cached    map[[sha256.Size]byte]requestCacheLocation
	cacheFile *os.File
	writer    *json.Encoder
	mu        sync.Mutex
}

func requestOracle(t testing.TB) (*requestOracleRunner, string) {
	t.Helper()
	runner := &requestOracleRunner{}
	if path := os.Getenv("YANG_ORACLE_CACHE"); path != "" {
		f, index, err := loadRequestCache(path)
		if err != nil {
			t.Fatal(err)
		}
		runner.cacheFile, runner.cached = f, index
		t.Cleanup(func() {
			if err := f.Close(); err != nil {
				t.Error(err)
			}
		})
	} else {
		runner.native = nativeRequestRunner(t)
	}
	if path := os.Getenv("YANG_ORACLE_CACHE_WRITE"); path != "" {
		if runner.cached != nil && nativeRequestAvailable() {
			runner.native = nativeRequestRunner(t)
		}
		f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600) //nolint:gosec // Explicit test-runner cache destination.
		if err != nil {
			t.Fatal(err)
		}
		w := bufio.NewWriter(f)
		runner.writer = json.NewEncoder(w)
		t.Cleanup(func() {
			if err := w.Flush(); err != nil {
				t.Error(err)
			}
			if err := f.Close(); err != nil {
				t.Error(err)
			}
		})
	}
	// Copy the corpus into an isolated working directory. Requests use relative
	// paths, so their exact JSON is identical in the container and offline. A file
	// mutation is recreated before each request by the shared generator.
	base := t.TempDir()
	if err := os.CopyFS(base, os.DirFS("corpus/protocol-v2")); err != nil {
		t.Fatal(err)
	}
	base, err := filepath.Abs(base)
	if err != nil {
		t.Fatal(err)
	}
	return runner, base
}

// loadRequestCache verifies the JSON-lines file in one streaming pass but retains only the
// request digest and record location. A replay therefore scales with the number of requests,
// not the size of their responses (the full pairwise capture is several gigabytes).
func loadRequestCache(path string) (*os.File, map[[sha256.Size]byte]requestCacheLocation, error) {
	f, err := os.Open(path) //nolint:gosec // Explicit test-runner cache, never request input.
	if err != nil {
		return nil, nil, err
	}
	fail := func(err error) (*os.File, map[[sha256.Size]byte]requestCacheLocation, error) {
		_ = f.Close()
		return nil, nil, err
	}
	index := map[[sha256.Size]byte]requestCacheLocation{}
	r := bufio.NewReader(f)
	var offset int64
	for {
		line, readErr := r.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) != 0 {
			var head struct {
				SHA256  string          `json:"sha256"`
				Request json.RawMessage `json:"request"`
			}
			if err := json.Unmarshal(line, &head); err != nil {
				return fail(fmt.Errorf("cache record at offset %d: %w", offset, err))
			}
			requestHash := sha256.Sum256(head.Request)
			if fmt.Sprintf("%x", requestHash) != head.SHA256 {
				return fail(fmt.Errorf("cache request digest mismatch: %s", head.SHA256))
			}
			loc := requestCacheLocation{offset: offset, length: int64(len(line))}
			if prior, ok := index[requestHash]; ok {
				if prior.length != loc.length {
					return fail(fmt.Errorf("cache request %s has non-identical duplicate records", head.SHA256))
				}
				priorLine := make([]byte, len(line))
				if _, err := f.ReadAt(priorLine, prior.offset); err != nil {
					return fail(fmt.Errorf("read duplicate cache request %s: %w", head.SHA256, err))
				}
				if sha256.Sum256(priorLine) != sha256.Sum256(line) {
					return fail(fmt.Errorf("cache request %s has non-identical duplicate records", head.SHA256))
				}
			} else {
				index[requestHash] = loc
			}
		}
		offset += int64(len(line))
		if readErr != nil {
			if readErr == io.EOF {
				break
			}
			return fail(readErr)
		}
	}
	return f, index, nil
}

func (r *requestOracleRunner) cacheEntry(loc requestCacheLocation) (requestCacheEntry, error) {
	b := make([]byte, int(loc.length))
	if _, err := r.cacheFile.ReadAt(b, loc.offset); err != nil {
		return requestCacheEntry{}, err
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var e requestCacheEntry
	if err := dec.Decode(&e); err != nil {
		return requestCacheEntry{}, err
	}
	return e, nil
}

func inventoryNativeResponse(r *requestOracleRunner, base string, p map[string]any) (Response, error) {
	b, err := json.Marshal(p)
	if err != nil {
		return nil, err
	}
	requestHash := sha256.Sum256(b)
	key := fmt.Sprintf("%x", requestHash)
	if r.cached != nil {
		loc, ok := r.cached[requestHash]
		if ok {
			e, err := r.cacheEntry(loc)
			if err != nil {
				return nil, fmt.Errorf("read oracle cache request %s: %w", key, err)
			}
			if e.Error != "" {
				return e.Response, fmt.Errorf("cached native error: %s", e.Error)
			}
			return e.Response, nil
		}
		if r.native == nil {
			return nil, fmt.Errorf("oracle cache missing request %s: %s", key, b)
		}
	}
	resp, nativeErr := r.native(base, p)
	if r.writer != nil {
		e := requestCacheEntry{SHA256: key, Request: b, Response: resp}
		if nativeErr != nil {
			e.Error = nativeErr.Error()
		}
		r.mu.Lock()
		defer r.mu.Unlock()
		if err := r.writer.Encode(e); err != nil {
			return nil, fmt.Errorf("write oracle cache: %w", err)
		}
	}
	return resp, nativeErr
}

// Cache transport preserves the entire response, exact JSON key, and failures;
// a missing entry must fail, never silently skip or run the engine as an oracle.
func TestRequestOracleCache(t *testing.T) {
	path := filepath.Join(t.TempDir(), "native.jsonl")
	f, err := os.Create(path) //nolint:gosec // Test-owned temporary file.
	if err != nil {
		t.Fatal(err)
	}
	p := map[string]any{"op": "data", "base_dir": ".", "data": ""}
	want := Response{"verdict": "invalid", "rc": map[string]any{"err": json.Number("7")}, "diagnostics": []any{map[string]any{"msg": "native message", "data_path": nil}}, "tree": nil}
	native := &requestOracleRunner{writer: json.NewEncoder(f), native: func(_ string, _ map[string]any) (Response, error) { return want, nil }}
	if _, err := inventoryNativeResponse(native, "", p); err != nil {
		t.Fatal(err)
	}
	native.native = func(_ string, _ map[string]any) (Response, error) { return nil, fmt.Errorf("oracle transport failed") }
	failed := map[string]any{"op": "sequence"}
	if _, err := inventoryNativeResponse(native, "", failed); err == nil {
		t.Fatal("transport failure lost")
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("YANG_ORACLE_CACHE", path)
	t.Setenv("YANG_ORACLE_CACHE_WRITE", "")
	replay, _ := requestOracle(t)
	got, err := inventoryNativeResponse(replay, "", p)
	wb, _ := json.Marshal(want)
	gb, _ := json.Marshal(got)
	if err != nil || string(wb) != string(gb) {
		t.Fatalf("round trip: %s, %v", gb, err)
	}
	if _, err := inventoryNativeResponse(replay, "", failed); err == nil {
		t.Fatal("cached transport failure lost")
	}
	p["data"] = nil
	if _, err := inventoryNativeResponse(replay, "", p); err == nil {
		t.Fatal("distinct missing request was accepted")
	}
}

// TestRequestCacheErrorTexts audits captured transport failures without replaying requests.
// Set YANG_ORACLE_CACHE_ERROR_SCAN to an OS-path-list of JSONL cache files.
func TestRequestCacheErrorTexts(t *testing.T) {
	paths := filepath.SplitList(os.Getenv("YANG_ORACLE_CACHE_ERROR_SCAN"))
	if len(paths) == 0 {
		t.Skip("set YANG_ORACLE_CACHE_ERROR_SCAN to cache JSONL paths")
	}
	registered := map[string]bool{}
	for _, class := range inventoryCrashClasses {
		registered[class.termination] = true
	}
	for _, path := range paths {
		counts, bad, err := scanRequestCacheErrorTexts(path, registered)
		if err != nil {
			t.Errorf("%s: %v", path, err)
			continue
		}
		t.Logf("%s: errors=%d registered=%d unregistered=%d", path, counts["errors"], counts["registered"], len(bad))
		for _, record := range bad {
			t.Errorf("%s", record)
		}
	}
}

func scanRequestCacheErrorTexts(path string, registered map[string]bool) (map[string]int, []string, error) {
	f, err := os.Open(path) //nolint:gosec // Explicit test-runner cache, never request input.
	if err != nil {
		return nil, nil, err
	}
	defer f.Close() //nolint:errcheck // Read-only audit; a close error cannot lose output.
	counts := map[string]int{}
	var bad []string
	r := bufio.NewReader(f)
	for line := 1; ; line++ {
		b, readErr := r.ReadBytes('\n')
		if len(bytes.TrimSpace(b)) != 0 {
			var record struct {
				SHA256 string `json:"sha256"`
				Error  string `json:"error"`
			}
			if err := json.Unmarshal(b, &record); err != nil {
				return nil, nil, fmt.Errorf("line %d: %w", line, err)
			}
			if record.Error != "" {
				counts["errors"]++
				termination := nativeTerminationText(errors.New(record.Error))
				if registered[termination] {
					counts["registered"]++
				} else {
					bad = append(bad, fmt.Sprintf("%s:%d %s: unregistered error %q", path, line, record.SHA256, record.Error))
				}
			}
		}
		if readErr != nil {
			if readErr == io.EOF {
				return counts, bad, nil
			}
			return nil, nil, readErr
		}
	}
}

func TestRequestOracleCacheTopUp(t *testing.T) {
	hit := map[string]any{"op": "data", "data": "hit"}
	miss := map[string]any{"op": "data", "data": "miss"}
	hitJSON, _ := json.Marshal(hit)
	hitKey := fmt.Sprintf("%x", sha256.Sum256(hitJSON))
	cachePath := filepath.Join(t.TempDir(), "cache.jsonl")
	cacheBytes, err := json.Marshal(requestCacheEntry{SHA256: hitKey, Request: hitJSON, Response: Response{"verdict": "cached"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cachePath, append(cacheBytes, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	cacheFile, cached, err := loadRequestCache(cachePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cacheFile.Close() })
	var appended bytes.Buffer
	nativeCalls := 0
	r := &requestOracleRunner{
		cached: cached, cacheFile: cacheFile,
		native: func(_ string, p map[string]any) (Response, error) {
			nativeCalls++
			return Response{"verdict": p["data"]}, nil
		},
		writer: json.NewEncoder(&appended),
	}
	if resp, err := inventoryNativeResponse(r, "", hit); err != nil || resp.Verdict() != "cached" || nativeCalls != 0 || appended.Len() != 0 {
		t.Fatalf("cache hit = %v, %v; native=%d append=%q", resp, err, nativeCalls, appended.String())
	}
	if resp, err := inventoryNativeResponse(r, "", miss); err != nil || resp.Verdict() != "miss" || nativeCalls != 1 || appended.Len() == 0 {
		t.Fatalf("cache miss top-up = %v, %v; native=%d append=%q", resp, err, nativeCalls, appended.String())
	}
	r.native = nil
	if _, err := inventoryNativeResponse(r, "", map[string]any{"op": "atoms"}); err == nil || !strings.Contains(err.Error(), "oracle cache missing request") {
		t.Fatalf("offline cache miss = %v", err)
	}
}

func TestRequestOracleCacheIndexValidation(t *testing.T) {
	p := json.RawMessage(`{"op":"data"}`)
	key := fmt.Sprintf("%x", sha256.Sum256(p))
	record := func(response string) []byte {
		return fmt.Appendf(nil, `{"sha256":%q,"request":%s,"response":{"verdict":%q}}`+"\n", key, p, response)
	}
	for _, tc := range []struct {
		name string
		data []byte
		want string
	}{
		{"bad-digest", bytes.Replace(record("valid"), []byte(key), []byte(strings.Repeat("0", 64)), 1), "digest mismatch"},
		{"different-duplicate", append(record("valid"), record("invalid")...), "non-identical duplicate"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "cache.jsonl")
			if err := os.WriteFile(path, tc.data, 0600); err != nil {
				t.Fatal(err)
			}
			if f, _, err := loadRequestCache(path); err == nil || !strings.Contains(err.Error(), tc.want) {
				if f != nil {
					_ = f.Close()
				}
				t.Fatalf("load = %v; want %q", err, tc.want)
			}
		})
	}

	path := filepath.Join(t.TempDir(), "identical.jsonl")
	line := record("valid")
	if err := os.WriteFile(path, append(slices.Clone(line), line...), 0600); err != nil {
		t.Fatal(err)
	}
	f, index, err := loadRequestCache(path)
	if err != nil || len(index) != 1 {
		t.Fatalf("identical duplicate = %d entries, %v", len(index), err)
	}
	if f != nil {
		_ = f.Close()
	}
}
