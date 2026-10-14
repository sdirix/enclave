// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package usage

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"enclave/internal/config"
	"enclave/internal/util"
)

// testSource parses lines of the form {"key":..,"ts":..,"output":..,"pending":..}.
type testSource struct{}

func (testSource) Dirs() []string { return []string{"logs"} }

func (testSource) Parse(r io.Reader) ([]Record, error) {
	var records []Record
	err := ScanLines(r, func(line []byte) {
		var entry struct {
			Key     string    `json:"key"`
			TS      time.Time `json:"ts"`
			Output  int64     `json:"output"`
			Pending bool      `json:"pending"`
		}
		if json.Unmarshal(line, &entry) != nil {
			return
		}
		records = append(records, Record{
			Key:       entry.Key,
			Timestamp: entry.TS,
			Model:     "m",
			Tokens:    Tokens{Input: 1, Output: entry.Output},
			Pending:   entry.Pending,
			Raw:       append(json.RawMessage(nil), line...),
		})
	})
	return records, err
}

func init() {
	RegisterSource("testtool", testSource{})
}

const testHash = "abcdef123456"

func writeLog(t *testing.T, home string, key string, name string, lines ...string) string {
	t.Helper()
	dir := filepath.Join(config.HostStoreConfigDir(home, "testtool", testHash, key), "logs")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func logLine(key string, ts string, output int, pending bool) string {
	data, _ := json.Marshal(map[string]any{"key": key, "ts": ts, "output": output, "pending": pending})
	return string(data)
}

func loadAll(t *testing.T, home string) []Record {
	t.Helper()
	records, skipped, err := Load(config.HostUsageDir(home), Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if skipped != 0 {
		t.Fatalf("skipped %d lines", skipped)
	}
	return records
}

func keys(records []Record) []string {
	var out []string
	for _, record := range records {
		out = append(out, record.Key)
	}
	return out
}

func TestScanLinesSkipsPartialAndOversizedLines(t *testing.T) {
	long := strings.Repeat("x", 100<<10)
	huge := strings.Repeat("y", MaxLineBytes+1)
	input := "a\n" + long + "\n" + huge + "\nb\npartial"
	var got []string
	if err := ScanLines(strings.NewReader(input), func(line []byte) {
		got = append(got, string(line))
	}); err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0] != "a" || got[1] != long || got[2] != "b" {
		t.Fatalf("lines = %d %q...", len(got), got[0])
	}
}

func TestHarvestArchivesOnceAndOutlivesStore(t *testing.T) {
	home := t.TempDir()
	writeLog(t, home, "default", "a.jsonl",
		logLine("k1", "2026-09-30T23:00:00Z", 5, false),
		logLine("k2", "2026-10-01T01:00:00Z", 7, false),
	)
	// A forked log repeats k2.
	writeLog(t, home, "session-x", "b.jsonl", logLine("k2", "2026-10-01T01:00:00Z", 7, false))

	added, err := HarvestAll(home, Options{Wait: true})
	if err != nil || added != 2 {
		t.Fatalf("added = %d, err = %v", added, err)
	}
	if added, err := HarvestAll(home, Options{Wait: true}); err != nil || added != 0 {
		t.Fatalf("second harvest added = %d, err = %v", added, err)
	}
	for _, month := range []string{"2026-09", "2026-10"} {
		if _, err := os.Stat(filepath.Join(config.HostUsageDir(home), month+".jsonl")); err != nil {
			t.Fatalf("missing %s archive: %v", month, err)
		}
	}

	if err := os.RemoveAll(config.HostProjectsDir(home)); err != nil {
		t.Fatal(err)
	}
	if _, err := HarvestAll(home, Options{Wait: true}); err != nil {
		t.Fatal(err)
	}
	records := loadAll(t, home)
	if got := keys(records); !slices.Equal(got, []string{"testtool:k1", "testtool:k2"}) {
		t.Fatalf("keys = %v", got)
	}
	r := records[0]
	if r.Tool != "testtool" || r.ProjectHash != testHash || r.Store != "default" || r.Raw != nil {
		t.Fatalf("record = %+v", r)
	}
	state := loadState(filepath.Join(config.HostUsageDir(home), stateFilename))
	if len(state.Files) != 0 {
		t.Fatalf("state keeps removed logs: %v", state.Files)
	}
}

func TestHarvestRecordsProjectFromMarker(t *testing.T) {
	home := t.TempDir()
	config.WriteProjectMarkers(home, testHash, "/work/demo")
	writeLog(t, home, "default", "a.jsonl", logLine("k1", "2026-10-01T00:00:00Z", 1, false))
	if _, err := HarvestAll(home, Options{Wait: true}); err != nil {
		t.Fatal(err)
	}
	if got := loadAll(t, home)[0].Project; got != "/work/demo" {
		t.Fatalf("project = %q", got)
	}
}

func TestHarvestDefersPendingRecordOfLiveLog(t *testing.T) {
	home := t.TempDir()
	path := writeLog(t, home, "default", "a.jsonl",
		logLine("k1", "2026-10-01T00:00:00Z", 5, false),
		logLine("k2", "2026-10-01T00:01:00Z", 1, true),
	)
	if _, err := HarvestAll(home, Options{Wait: true}); err != nil {
		t.Fatal(err)
	}
	if got := keys(loadAll(t, home)); !slices.Equal(got, []string{"testtool:k1"}) {
		t.Fatalf("keys after live harvest = %v", got)
	}

	// The response completes with its final count.
	writeLog(t, home, "default", "a.jsonl",
		logLine("k1", "2026-10-01T00:00:00Z", 5, false),
		logLine("k2", "2026-10-01T00:01:00Z", 90, false),
	)
	if _, err := HarvestAll(home, Options{Wait: true}); err != nil {
		t.Fatal(err)
	}
	records := loadAll(t, home)
	if len(records) != 2 || records[1].Tokens.Output != 90 {
		t.Fatalf("records = %+v", records)
	}

	// An interrupted response is archived once the log is old enough.
	writeLog(t, home, "default", "a.jsonl", logLine("k3", "2026-10-01T00:02:00Z", 3, true))
	old := time.Now().Add(-2 * pendingGrace)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	if _, err := HarvestAll(home, Options{Wait: true}); err != nil {
		t.Fatal(err)
	}
	if got := len(loadAll(t, home)); got != 3 {
		t.Fatalf("records = %d, want the settled pending one archived", got)
	}
}

func TestHarvestOfClosedStoreArchivesPendingRecord(t *testing.T) {
	home := t.TempDir()
	writeLog(t, home, "s1", "a.jsonl", logLine("k1", "2026-10-01T00:00:00Z", 1, true))
	store := []Store{{Tool: "testtool", ProjectHash: testHash, Key: "s1"}}
	// Another session may still be writing a persistent store.
	if added, err := Harvest(home, store, Options{Wait: true}); err != nil || added != 0 {
		t.Fatalf("added = %d, err = %v", added, err)
	}
	added, err := Harvest(home, store, Options{Removing: true, Wait: true})
	if err != nil || added != 1 {
		t.Fatalf("added = %d, err = %v", added, err)
	}
}

func TestHarvestSkipsWhenBusyUnlessWaiting(t *testing.T) {
	home := t.TempDir()
	writeLog(t, home, "default", "a.jsonl", logLine("k1", "2026-10-01T00:00:00Z", 1, false))
	release, err := util.AcquireFileLock(config.HostLockPath(home, lockName), nil)
	if err != nil {
		t.Fatal(err)
	}
	store := []Store{{Tool: "testtool", ProjectHash: testHash}}
	if added, err := Harvest(home, store, Options{}); err != nil || added != 0 {
		t.Fatalf("busy harvest added = %d, err = %v", added, err)
	}
	release()
	if added, err := Harvest(home, store, Options{}); err != nil || added != 1 {
		t.Fatalf("free harvest added = %d, err = %v", added, err)
	}
}

func TestHarvestDefaultStoreKey(t *testing.T) {
	home := t.TempDir()
	writeLog(t, home, "default", "a.jsonl", logLine("k1", "2026-10-01T00:00:00Z", 1, false))
	added, err := Harvest(home, []Store{{Tool: "testtool", ProjectHash: testHash}}, Options{Wait: true})
	if err != nil || added != 1 {
		t.Fatalf("added = %d, err = %v", added, err)
	}
}

func TestHarvestSkipsSymlinkedLogs(t *testing.T) {
	home := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret.jsonl")
	if err := os.WriteFile(outside, []byte(logLine("k1", "2026-10-01T00:00:00Z", 1, false)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := writeLog(t, home, "default", "a.jsonl")
	if err := os.Symlink(outside, filepath.Join(filepath.Dir(path), "link.jsonl")); err != nil {
		t.Fatal(err)
	}
	if added, err := HarvestAll(home, Options{Wait: true}); err != nil || added != 0 {
		t.Fatalf("added = %d, err = %v", added, err)
	}
}

func TestHarvestStaysInsideStore(t *testing.T) {
	home := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "a.jsonl"), []byte(logLine("k1", "2026-10-01T00:00:00Z", 1, false)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	storeDir := config.HostStoreConfigDir(home, "testtool", testHash, "default")
	if err := os.MkdirAll(storeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(storeDir, "logs")); err != nil {
		t.Fatal(err)
	}
	if added, _ := HarvestAll(home, Options{Wait: true}); added != 0 {
		t.Fatalf("added = %d from outside the store", added)
	}
}

func TestHarvestErrorsAreDisplaySafe(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs a log the current user cannot read")
	}
	home := t.TempDir()
	path := writeLog(t, home, "default", "evil\x1b[2J.jsonl", logLine("k1", "2026-10-01T01:00:00Z", 5, false))
	if err := os.Chmod(path, 0); err != nil {
		t.Fatal(err)
	}
	_, err := HarvestAll(home, Options{Wait: true})
	if err == nil || strings.ContainsRune(err.Error(), '\x1b') || !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("err = %v", err)
	}
}

func TestStoresUnder(t *testing.T) {
	home := t.TempDir()
	writeLog(t, home, "default", "a.jsonl")
	writeLog(t, home, "s1", "a.jsonl")
	root := config.HostStoreConfigRootDir(home, "testtool", testHash)
	if got := StoresUnder(home, []string{root}); len(got) != 2 {
		t.Fatalf("stores under root = %+v", got)
	}
	got := StoresUnder(home, []string{filepath.Join(root, "s1")})
	if len(got) != 1 || got[0].Key != "s1" {
		t.Fatalf("stores under s1 = %+v", got)
	}
	if got := StoresUnder(home, []string{root + "-other"}); len(got) != 0 {
		t.Fatalf("prefix match leaked: %+v", got)
	}
}

func TestArchiveDropsTornLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "2026-10.jsonl")
	ts := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	whole, _ := json.Marshal(Record{Key: "testtool:k0", Tool: "testtool", Timestamp: ts})
	torn := `{"key":"testtool:torn","tool":"te`
	if err := os.WriteFile(path, append(append(whole, '\n'), torn...), 0o600); err != nil {
		t.Fatal(err)
	}
	added, err := newArchive(dir).add([]Record{{Key: "testtool:k1", Tool: "testtool", Timestamp: ts}})
	if err != nil || added != 1 {
		t.Fatalf("added = %d, err = %v", added, err)
	}
	// The torn record was never written whole, so it can still be added.
	added, err = newArchive(dir).add([]Record{{Key: "testtool:torn", Tool: "testtool", Timestamp: ts}})
	if err != nil || added != 1 {
		t.Fatalf("re-adding torn record: added = %d, err = %v", added, err)
	}
	records, skipped, err := Load(dir, Filter{})
	if err != nil || skipped != 0 || !slices.Equal(keys(records), []string{"testtool:k0", "testtool:k1", "testtool:torn"}) {
		t.Fatalf("records = %v, skipped = %d, err = %v", keys(records), skipped, err)
	}
}

func TestArchiveKeyReplacesUnsafeKeys(t *testing.T) {
	if got := archiveKey("msg_01:req_02"); got != "msg_01:req_02" {
		t.Fatalf("safe key changed to %q", got)
	}
	got := archiveKey(`a"b`)
	if !strings.HasPrefix(got, "sha256-") {
		t.Fatalf("unsafe key kept as %q", got)
	}
	line, _ := json.Marshal(Record{Key: got})
	if lineKey(line) != got {
		t.Fatalf("lineKey(%s) != %q", line, got)
	}
	if got := archiveKey(strings.Repeat("k", maxKeyBytes+1)); !strings.HasPrefix(got, "sha256-") {
		t.Fatalf("long key kept as %q", got)
	}
}

func TestDropOversizedClearsLongFields(t *testing.T) {
	long := strings.Repeat("x", maxFieldBytes+1)
	r := Record{Model: long, Cwd: "/work", GitBranch: long, Raw: json.RawMessage(`"` + strings.Repeat("x", MaxRawBytes) + `"`)}
	r.dropOversized()
	if r.Model != "" || r.GitBranch != "" || r.Cwd != "/work" || r.Raw != nil {
		t.Fatalf("record = %+v", r)
	}
	r = Record{Model: "m", Raw: json.RawMessage(`{}`)}
	r.dropOversized()
	if r.Model != "m" || string(r.Raw) != "{}" {
		t.Fatalf("record = %+v", r)
	}
}

func TestHarvestDropsOversizedRawLine(t *testing.T) {
	home := t.TempDir()
	padded, _ := json.Marshal(map[string]any{"key": "k1", "ts": "2026-10-01T01:00:00Z", "pad": strings.Repeat("x", MaxRawBytes)})
	writeLog(t, home, "default", "a.jsonl", string(padded))
	if added, err := HarvestAll(home, Options{Wait: true}); err != nil || added != 1 {
		t.Fatalf("added = %d, err = %v", added, err)
	}
	info, err := os.Stat(filepath.Join(config.HostUsageDir(home), "2026-10.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() > 4<<10 {
		t.Fatalf("archive keeps the oversized line: %d bytes", info.Size())
	}
}

func TestLineKeyFallsBackToDecoding(t *testing.T) {
	if got := lineKey([]byte(`{"tool":"x","key":"k"}`)); got != "k" {
		t.Fatalf("lineKey = %q", got)
	}
	if got := lineKey([]byte(`not json`)); got != "" {
		t.Fatalf("lineKey = %q", got)
	}
}

func TestLoadFilters(t *testing.T) {
	dir := t.TempDir()
	a := newArchive(dir)
	day := func(d int) time.Time { return time.Date(2026, 9, d, 12, 0, 0, 0, time.UTC) }
	_, err := a.add([]Record{
		{Key: "x:1", Tool: "x", Timestamp: day(1)},
		{Key: "y:2", Tool: "y", Timestamp: day(2)},
		{Key: "x:3", Tool: "x", Timestamp: day(3)},
		{Key: "x:4", Tool: "x", Timestamp: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)},
	})
	if err != nil {
		t.Fatal(err)
	}
	records, _, err := Load(dir, Filter{Since: day(2), Until: day(3), Tool: "y"})
	if err != nil || len(records) != 1 || records[0].Key != "y:2" {
		t.Fatalf("records = %+v, err = %v", records, err)
	}
	records, _, err = Load(dir, Filter{Since: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)})
	if err != nil || len(records) != 1 || records[0].Key != "x:4" {
		t.Fatalf("records = %+v, err = %v", records, err)
	}
}

func TestSummarize(t *testing.T) {
	ts := time.Date(2026, 10, 1, 23, 30, 0, 0, time.UTC)
	records := []Record{
		{Model: "small", Timestamp: ts, Tokens: Tokens{Input: 1, Output: 1}},
		{Model: "big", Timestamp: ts.Add(time.Hour), Tokens: Tokens{Input: 10, CacheRead: 100, Output: 5, Reasoning: 2}},
		{Model: "big", Timestamp: ts.Add(2 * time.Hour), Tokens: Tokens{CacheWrite: 50}},
	}
	rows, total, err := Summarize(records, GroupModel, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].Key != "big" || rows[0].Requests != 2 || rows[0].Tokens.Total != 165 {
		t.Fatalf("rows = %+v", rows)
	}
	if total.Requests != 3 || total.Tokens.Total != 167 || total.Tokens.Reasoning != 2 {
		t.Fatalf("total = %+v", total)
	}
	rows, _, _ = Summarize(records, GroupDay, time.UTC)
	if len(rows) != 2 || rows[0].Key != "2026-10-01" || rows[1].Key != "2026-10-02" {
		t.Fatalf("day rows = %+v", rows)
	}
	if _, _, err := Summarize(records, "week", time.UTC); err == nil {
		t.Fatal("expected error for unknown grouping")
	}
}

func TestRecordKeyIsFirstField(t *testing.T) {
	line, err := json.Marshal(Record{Key: "k", Tool: "t"})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(line, keyPrefix) {
		t.Fatalf("record encodes as %s", line)
	}
}
