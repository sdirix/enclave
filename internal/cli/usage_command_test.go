// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package cli

import (
	"strings"
	"testing"

	"enclave/internal/config"
	"enclave/internal/model"
)

func TestParseUsageFlags(t *testing.T) {
	res, err := Parse([]string{
		"usage", "--by", "Model", "--since", "30d", "--until", "2026-10-01", "--json", "--tool", "codex",
	}, config.DefaultOptions())
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if res.Action != "usage" {
		t.Fatalf("action = %q, want usage", res.Action)
	}
	want := model.UsageView{By: "model", Since: "30d", Until: "2026-10-01", JSON: true}
	if res.UsageView != want {
		t.Fatalf("view = %+v, want %+v", res.UsageView, want)
	}
	if res.Options.Tool != "codex" || res.Sources.Tool != model.SourceCLI {
		t.Fatalf("tool = %q from %q", res.Options.Tool, res.Sources.Tool)
	}
}

func TestParseUsageDefaultsToDay(t *testing.T) {
	res, err := Parse([]string{"usage"}, config.DefaultOptions())
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if res.UsageView.By != "day" {
		t.Fatalf("by = %q, want day", res.UsageView.By)
	}
}

func TestParseUsageRejectsUnknownGrouping(t *testing.T) {
	_, err := Parse([]string{"usage", "--by", "week"}, config.DefaultOptions())
	if err == nil || !strings.Contains(err.Error(), "invalid --by") {
		t.Fatalf("expected invalid --by error, got %v", err)
	}
}
