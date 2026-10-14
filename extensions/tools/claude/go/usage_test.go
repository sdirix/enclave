// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package claude

import (
	"strconv"
	"strings"
	"testing"

	"enclave/internal/usage"
)

func assistantLine(id string, output int, stop string, content string) string {
	stopReason := "null"
	if stop != "" {
		stopReason = `"` + stop + `"`
	}
	return `{"type":"assistant","uuid":"u-` + id + `","timestamp":"2026-10-01T10:00:00.000Z","requestId":"req_` + id +
		`","sessionId":"s1","cwd":"/work","gitBranch":"main","version":"2.1.0","effort":"high","isSidechain":true,` +
		`"message":{"id":"msg_` + id + `","model":"claude-opus-5-5","stop_reason":` + stopReason +
		`,"content":[` + content + `],"usage":{"input_tokens":2,"cache_creation_input_tokens":100,` +
		`"cache_read_input_tokens":1000,"output_tokens":` + strconv.Itoa(output) + `,"output_tokens_details":{"thinking_tokens":3},` +
		`"service_tier":"standard"}}}`
}

func parse(t *testing.T, lines ...string) []usage.Record {
	t.Helper()
	records, err := usageSource{}.Parse(strings.NewReader(strings.Join(lines, "\n") + "\n"))
	if err != nil {
		t.Fatal(err)
	}
	return records
}

func TestUsageParseKeepsFinalCountOfStreamedResponse(t *testing.T) {
	records := parse(t,
		assistantLine("a", 8, "", `{"type":"thinking","thinking":"secret plan"}`),
		`{"type":"user","message":{"role":"user","content":"tool result"}}`,
		assistantLine("a", 283, "tool_use", `{"type":"tool_use","name":"Bash"}`),
	)
	if len(records) != 1 {
		t.Fatalf("records = %d, want 1", len(records))
	}
	r := records[0]
	if r.Key != "msg_a:req_a" || r.Pending {
		t.Fatalf("key = %q pending = %v", r.Key, r.Pending)
	}
	want := usage.Tokens{Input: 2, CacheWrite: 100, CacheRead: 1000, Output: 283, Reasoning: 3}
	if r.Tokens != want {
		t.Fatalf("tokens = %+v, want %+v", r.Tokens, want)
	}
	if r.Model != "claude-opus-5-5" || r.Session != "s1" || r.Cwd != "/work" || r.GitBranch != "main" || r.Version != "2.1.0" || r.Effort != "high" {
		t.Fatalf("record = %+v", r)
	}
	raw := string(r.Raw)
	if strings.Contains(raw, "content") || strings.Contains(raw, "Bash") {
		t.Fatalf("raw keeps conversation content: %s", raw)
	}
	for _, field := range []string{`"isSidechain":true`, `"service_tier":"standard"`, `"stop_reason":"tool_use"`} {
		if !strings.Contains(raw, field) {
			t.Fatalf("raw lacks %s: %s", field, raw)
		}
	}
}

func TestUsageParseStripsLongLine(t *testing.T) {
	text := strings.Repeat("x", usage.MaxRawBytes)
	records := parse(t, assistantLine("a", 8, "end_turn", `{"type":"text","text":"`+text+`"}`))
	if len(records) != 1 {
		t.Fatalf("records = %d, want 1", len(records))
	}
	raw := string(records[0].Raw)
	if strings.Contains(raw, "xxx") || !strings.Contains(raw, `"stop_reason":"end_turn"`) {
		t.Fatalf("raw = %.200s", raw)
	}
}

func TestUsageParseMarksOnlyUnfinishedTailPending(t *testing.T) {
	records := parse(t,
		assistantLine("interrupted", 4, "", ""),
		assistantLine("done", 10, "end_turn", ""),
		assistantLine("streaming", 1, "", ""),
	)
	if len(records) != 3 {
		t.Fatalf("records = %d, want 3", len(records))
	}
	if records[0].Pending || records[1].Pending || !records[2].Pending {
		t.Fatalf("pending = %v %v %v", records[0].Pending, records[1].Pending, records[2].Pending)
	}
}

func TestUsageParseSkipsSyntheticAndNonAssistantLines(t *testing.T) {
	synthetic := strings.Replace(assistantLine("x", 0, "stop_sequence", ""), "claude-opus-5-5", "<synthetic>", 1)
	records := parse(t,
		synthetic,
		`{"type":"attachment","usage":{"input_tokens":5}}`,
		`{"type":"summary","summary":"usage of tools"}`,
		`not json "usage"`,
	)
	if len(records) != 0 {
		t.Fatalf("records = %+v", records)
	}
}

func TestUsageParseSumsIterations(t *testing.T) {
	line := strings.Replace(assistantLine("c", 40, "end_turn", ""), `"service_tier":"standard"`,
		`"service_tier":"standard","iterations":[`+
			`{"type":"compaction","input_tokens":5000,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":300},`+
			`{"type":"message","input_tokens":2,"cache_creation_input_tokens":100,"cache_read_input_tokens":1000,"output_tokens":40}]`, 1)
	records := parse(t, line)
	if len(records) != 1 {
		t.Fatalf("records = %d, want 1", len(records))
	}
	want := usage.Tokens{Input: 5002, CacheWrite: 100, CacheRead: 1000, Output: 340, Reasoning: 3}
	if records[0].Tokens != want {
		t.Fatalf("tokens = %+v, want %+v", records[0].Tokens, want)
	}
}
