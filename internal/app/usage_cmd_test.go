// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

import (
	"bytes"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"enclave/internal/usage"
)

func TestParseUsageBound(t *testing.T) {
	now := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	day := time.Date(2026, 10, 1, 0, 0, 0, 0, time.Local)
	for _, tc := range []struct {
		value string
		upper bool
		want  time.Time
	}{
		{value: "", want: time.Time{}},
		{value: "2026-10-01", want: day},
		{value: "2026-10-01", upper: true, want: day.AddDate(0, 0, 1)},
		{value: "2026-10-01T08:00:00Z", want: time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)},
		{value: "12h", want: now.Add(-12 * time.Hour)},
		{value: "30d", want: now.AddDate(0, 0, -30)},
	} {
		got, err := parseUsageBound(tc.value, now, tc.upper)
		if err != nil || !got.Equal(tc.want) {
			t.Fatalf("parseUsageBound(%q, upper=%v) = %v, %v; want %v", tc.value, tc.upper, got, err, tc.want)
		}
	}
	for _, value := range []string{"yesterday", "-3d", "-1h", "10x"} {
		if _, err := parseUsageBound(value, now, false); err == nil {
			t.Fatalf("parseUsageBound(%q) accepted", value)
		}
	}
}

func TestGroupDigits(t *testing.T) {
	for n, want := range map[int64]string{0: "0", 999: "999", 1000: "1,000", 1234567: "1,234,567", -45000: "-45,000"} {
		if got := groupDigits(n); got != want {
			t.Fatalf("groupDigits(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestRenderUsageTable(t *testing.T) {
	sum := func(requests int64, input int64) usage.Sum {
		return usage.Sum{Requests: requests, Tokens: usage.Totals{Tokens: usage.Tokens{Input: input}, Total: input}}
	}
	rows := []usage.Row{
		{Key: "claude-opus-5-5", Sum: sum(2, 1500)},
		{Key: "gpt", Sum: sum(1, 7)},
	}
	var out bytes.Buffer
	renderUsageTable(&out, usage.GroupModel, rows, sum(3, 1507))
	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	if len(lines) != 5 || lines[3] != "" {
		t.Fatalf("table:\n%s", out.String())
	}
	if !strings.HasPrefix(lines[0], "MODEL            REQUESTS") || !strings.HasPrefix(lines[4], "TOTAL ") {
		t.Fatalf("table:\n%s", out.String())
	}
	// Numeric columns are right-aligned to the header width.
	if !strings.Contains(lines[1], "  1,500  ") || len(lines[1]) != len(lines[0]) {
		t.Fatalf("table:\n%s", out.String())
	}
}

func TestRenderUsageTableNeutralizesKeys(t *testing.T) {
	sum := usage.Sum{Requests: 1, Tokens: usage.Totals{Tokens: usage.Tokens{Input: 7}, Total: 7}}
	rows := []usage.Row{
		{Key: "evil\x1b]52;c;Zm9v\a", Sum: sum},
		{Key: "/home/jörg/projekt", Sum: sum},
	}
	var out bytes.Buffer
	renderUsageTable(&out, usage.GroupModel, rows, sum)
	if strings.ContainsAny(out.String(), "\x1b\a") {
		t.Fatalf("table keeps control characters: %q", out.String())
	}
	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	for _, line := range lines[1:3] {
		if utf8.RuneCountInString(line) != utf8.RuneCountInString(lines[0]) {
			t.Fatalf("misaligned table:\n%s", out.String())
		}
	}
}
