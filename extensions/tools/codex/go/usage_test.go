// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package codex

import (
	"strconv"
	"strings"
	"testing"

	"enclave/internal/usage"
)

const (
	sessionMetaLine = `{"timestamp":"2026-10-01T10:00:00.000Z","type":"session_meta","payload":{"id":"thread-1","session_id":"root-1","cwd":"/work","cli_version":"0.200.0","git":{"branch":"main"}}}`
	parentMetaLine  = `{"timestamp":"2026-10-01T10:00:00.000Z","type":"session_meta","payload":{"id":"parent","session_id":"parent","cwd":"/elsewhere","cli_version":"0.100.0"}}`
	turnLine        = `{"timestamp":"2026-10-01T10:00:01.000Z","type":"turn_context","payload":{"turn_id":"t1","cwd":"/work/sub","model":"gpt-6-codex","effort":"high","approval_policy":"never"}}`
	turnLine2       = `{"timestamp":"2026-10-01T10:05:00.000Z","type":"turn_context","payload":{"turn_id":"t2","cwd":"/work","model":"gpt-6-mini","effort":"low"}}`
)

func parse(t *testing.T, lines ...string) []usage.Record {
	t.Helper()
	records, err := usageSource{}.Parse(strings.NewReader(strings.Join(lines, "\n") + "\n"))
	if err != nil {
		t.Fatal(err)
	}
	return records
}

func usageRecordLine(turn string, response string) string {
	return `{"timestamp":"2026-10-01T10:00:02.000Z","type":"token_usage_record","payload":{"thread_id":"thread-1","turn_id":"` + turn +
		`","session_id":"root-1","root_turn_id":"` + turn + `","response_id":"` + response +
		`","usage":{"input_tokens":1000,"cached_input_tokens":600,"cache_write_input_tokens":100,"output_tokens":50,"reasoning_output_tokens":20,"total_tokens":1050},` +
		`"turn_token_usage":{"input_tokens":0,"cached_input_tokens":0,"output_tokens":0,"reasoning_output_tokens":0,"total_tokens":0},` +
		`"thread_token_usage":{"input_tokens":0,"cached_input_tokens":0,"output_tokens":0,"reasoning_output_tokens":0,"total_tokens":0}}}`
}

func tokenCountLine(totalInput int, lastInput int) string {
	total := `{"input_tokens":` + strconv.Itoa(totalInput) + `,"cached_input_tokens":0,"output_tokens":10,"reasoning_output_tokens":0,"total_tokens":` + strconv.Itoa(totalInput+10) + `}`
	last := `{"input_tokens":` + strconv.Itoa(lastInput) + `,"cached_input_tokens":0,"output_tokens":10,"reasoning_output_tokens":0,"total_tokens":` + strconv.Itoa(lastInput+10) + `}`
	return `{"timestamp":"2026-10-01T10:00:03.000Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":` + total +
		`,"last_token_usage":` + last + `,"model_context_window":272000},"rate_limits":{"primary":{"used_percent":12.5}}}}`
}

func TestUsageParsePrefersTokenUsageRecords(t *testing.T) {
	records := parse(t,
		sessionMetaLine,
		parentMetaLine,
		turnLine,
		turnLine2,
		usageRecordLine("t1", "resp_1"),
		tokenCountLine(1000, 1000),
	)
	if len(records) != 1 {
		t.Fatalf("records = %d, want 1", len(records))
	}
	r := records[0]
	if r.Key != "resp_1" || r.Model != "gpt-6-codex" || r.Effort != "high" || r.Cwd != "/work/sub" {
		t.Fatalf("record = %+v", r)
	}
	if r.Session != "root-1" || r.GitBranch != "main" || r.Version != "0.200.0" {
		t.Fatalf("session context = %+v", r)
	}
	want := usage.Tokens{Input: 300, CacheWrite: 100, CacheRead: 600, Output: 50, Reasoning: 20}
	if r.Tokens != want {
		t.Fatalf("tokens = %+v, want %+v", r.Tokens, want)
	}
	if !strings.Contains(string(r.Raw), `"response_id":"resp_1"`) {
		t.Fatalf("raw = %s", r.Raw)
	}
}

func TestUsageParseFallsBackToTokenCountEvents(t *testing.T) {
	records := parse(t,
		sessionMetaLine,
		turnLine,
		tokenCountLine(1000, 1000),
		// A rate-limit update repeats the totals.
		tokenCountLine(1000, 1000),
		tokenCountLine(3000, 2000),
	)
	if len(records) != 2 {
		t.Fatalf("records = %d, want 2", len(records))
	}
	if records[0].Key == records[1].Key || !strings.HasPrefix(records[0].Key, "thread-1:") {
		t.Fatalf("keys = %q %q", records[0].Key, records[1].Key)
	}
	if records[1].Tokens.Input != 2000 || records[1].Model != "gpt-6-codex" || records[1].Session != "root-1" {
		t.Fatalf("record = %+v", records[1])
	}
	if !strings.Contains(string(records[0].Raw), "rate_limits") {
		t.Fatalf("raw lacks rate limits: %s", records[0].Raw)
	}
}

const forkMetaLine = `{"timestamp":"2026-11-01T10:00:00.000Z","type":"session_meta","payload":{"id":"fork-1","session_id":"fork-1","cwd":"/work","cli_version":"0.200.0","forked_from_id":"thread-1"}}`

func TestUsageParseSkipsRecordsCopiedIntoFork(t *testing.T) {
	copied := strings.Replace(usageRecordLine("t1", "resp_1"), `"timestamp":"2026-10-01T10:00:02.000Z"`, `"timestamp":"2026-11-01T10:00:00.500Z"`, 1)
	own := strings.Replace(strings.Replace(usageRecordLine("t9", "resp_9"), `"thread_id":"thread-1"`, `"thread_id":"fork-1"`, 1),
		`"timestamp":"2026-10-01T10:00:02.000Z"`, `"timestamp":"2026-11-01T10:01:00.000Z"`, 1)
	records := parse(t, forkMetaLine, sessionMetaLine, turnLine, copied, own)
	if len(records) != 1 || records[0].Key != "resp_9" {
		t.Fatalf("records = %+v", records)
	}

	// A fork without own responses yet must not fall back to copied events.
	if records := parse(t, forkMetaLine, sessionMetaLine, copied, tokenCountLine(1000, 1000)); len(records) != 0 {
		t.Fatalf("records = %+v", records)
	}
}

func TestUsageParseKeysCopiedTokenCountsByParent(t *testing.T) {
	parent := parse(t, sessionMetaLine, turnLine, tokenCountLine(1000, 1000))
	fork := parse(t, forkMetaLine, sessionMetaLine, turnLine, tokenCountLine(1000, 1000))
	if len(parent) != 1 || len(fork) != 1 || parent[0].Key != fork[0].Key {
		t.Fatalf("parent = %+v, fork = %+v", parent, fork)
	}
}
