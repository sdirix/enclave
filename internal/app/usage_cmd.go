// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"enclave/internal/config"
	"enclave/internal/logx"
	"enclave/internal/model"
	"enclave/internal/usage"
	"enclave/internal/util"
)

const usageSchemaVersion = "1"

// usageReport is the `usage --json` envelope. Add fields, never rename or
// remove them.
type usageReport struct {
	SchemaVersion string      `json:"schemaVersion"`
	GroupBy       string      `json:"groupBy"`
	Since         *time.Time  `json:"since,omitempty"`
	Until         *time.Time  `json:"until,omitempty"`
	Tool          string      `json:"tool,omitempty"`
	Rows          []usage.Row `json:"rows"`
	Total         usage.Sum   `json:"total"`
}

func runUsage(opts model.Options, view model.UsageView) int {
	home, err := config.ResolveHostHome()
	if err != nil {
		logx.Errorf("Failed to resolve home directory: %v", err)
		return 1
	}
	now := time.Now()
	since, err := parseUsageBound(view.Since, now, false)
	if err != nil {
		logx.Errorf("Invalid --since: %v", err)
		return 1
	}
	until, err := parseUsageBound(view.Until, now, true)
	if err != nil {
		logx.Errorf("Invalid --until: %v", err)
		return 1
	}
	if !since.IsZero() && !until.IsZero() && !since.Before(until) {
		logx.Errorf("--since must be before --until")
		return 1
	}

	// Progress notes go to stdout, which belongs to the JSON document.
	harvestOpts := usage.Options{Wait: true}
	if !view.JSON {
		harvestOpts.Notify = logx.Infof
	}
	if _, err := usage.HarvestAll(home, harvestOpts); err != nil {
		logx.Warnf("Failed to archive some token usage: %v", err)
	}
	dir := config.HostUsageDir(home)
	filter := usage.Filter{Since: since, Until: until, Tool: psToolFilter(opts)}
	records, skipped, err := usage.Load(dir, filter)
	if err != nil {
		logx.Errorf("Failed to read usage archive: %v", err)
		return 1
	}
	if skipped > 0 {
		logx.Warnf("Skipped %d malformed line(s) in %s", skipped, dir)
	}
	rows, total, err := usage.Summarize(records, view.By, time.Local)
	if err != nil {
		logx.Errorf("%v", err)
		return 1
	}

	if view.JSON {
		report := usageReport{
			SchemaVersion: usageSchemaVersion,
			GroupBy:       view.By,
			Tool:          filter.Tool,
			Rows:          append([]usage.Row{}, rows...),
			Total:         total,
		}
		if !since.IsZero() {
			report.Since = &since
		}
		if !until.IsZero() {
			report.Until = &until
		}
		if err := encodeIndentedJSON(os.Stdout, report); err != nil {
			logx.Errorf("Failed to encode usage: %v", err)
			return 1
		}
		return 0
	}
	if len(rows) == 0 {
		logx.Infof("No token usage recorded")
		return 0
	}
	renderUsageTable(os.Stdout, view.By, rows, total)
	return 0
}

// parseUsageBound accepts a local date, an RFC3339 timestamp, or a duration
// before now with an optional day unit. A date as the upper bound includes
// that whole day.
func parseUsageBound(value string, now time.Time, upper bool) (time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, nil
	}
	if day, err := time.ParseInLocation(time.DateOnly, value, time.Local); err == nil {
		if upper {
			return day.AddDate(0, 0, 1), nil
		}
		return day, nil
	}
	if ts, err := time.Parse(time.RFC3339, value); err == nil {
		return ts, nil
	}
	if days, ok := strings.CutSuffix(value, "d"); ok {
		if n, err := strconv.Atoi(days); err == nil && n >= 0 {
			return now.AddDate(0, 0, -n), nil
		}
	} else if d, err := time.ParseDuration(value); err == nil && d >= 0 {
		return now.Add(-d), nil
	}
	return time.Time{}, fmt.Errorf("%q is not a date (2026-10-01), RFC3339 timestamp, or duration (12h, 30d)", value)
}

// renderUsageTable neutralizes the row keys, since model names come from the
// agent-writable session logs.
func renderUsageTable(w io.Writer, groupBy string, rows []usage.Row, total usage.Sum) {
	header := []string{strings.ToUpper(groupBy), "REQUESTS", "INPUT", "CACHE WRITE", "CACHE READ", "OUTPUT", "TOTAL"}
	cells := func(key string, sum usage.Sum) []string {
		t := sum.Tokens
		return []string{util.DisplaySafe(key), groupDigits(sum.Requests), groupDigits(t.Input), groupDigits(t.CacheWrite),
			groupDigits(t.CacheRead), groupDigits(t.Output), groupDigits(t.Total)}
	}
	lines := [][]string{header}
	for _, row := range rows {
		lines = append(lines, cells(row.Key, row.Sum))
	}
	lines = append(lines, cells("TOTAL", total))

	widths := make([]int, len(header))
	for _, line := range lines {
		for i, cell := range line {
			widths[i] = max(widths[i], utf8.RuneCountInString(cell))
		}
	}
	for n, line := range lines {
		if n == len(lines)-1 {
			_, _ = fmt.Fprintln(w)
		}
		var b strings.Builder
		for i, cell := range line {
			if i == 0 {
				fmt.Fprintf(&b, "%-*s", widths[i], cell)
				continue
			}
			fmt.Fprintf(&b, "  %*s", widths[i], cell)
		}
		_, _ = fmt.Fprintln(w, b.String())
	}
}

func groupDigits(n int64) string {
	digits := strconv.FormatInt(n, 10)
	sign := ""
	if strings.HasPrefix(digits, "-") {
		sign, digits = "-", digits[1:]
	}
	var b strings.Builder
	for i, r := range digits {
		if i > 0 && (len(digits)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	return sign + b.String()
}
