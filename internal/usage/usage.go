// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

// Package usage archives model token usage from tool session logs into a
// host-side store that outlives config stores, and aggregates it for reports.
package usage

import (
	"encoding/json"
	"io"
	"sort"
	"strings"
	"sync"
	"time"
)

// Record is one model response with its token counts. Key must come first:
// the archive reads keys from the line prefix without decoding the record.
type Record struct {
	Key         string          `json:"key"`
	Tool        string          `json:"tool"`
	Timestamp   time.Time       `json:"timestamp"`
	Model       string          `json:"model,omitempty"`
	Session     string          `json:"session,omitempty"`
	Project     string          `json:"project,omitempty"`
	ProjectHash string          `json:"projectHash,omitempty"`
	Store       string          `json:"store,omitempty"`
	Cwd         string          `json:"cwd,omitempty"`
	GitBranch   string          `json:"gitBranch,omitempty"`
	Version     string          `json:"version,omitempty"`
	Effort      string          `json:"effort,omitempty"`
	Tokens      Tokens          `json:"tokens"`
	Raw         json.RawMessage `json:"raw,omitempty"`
	// Pending marks a response that may still be streaming, so its counts can
	// still grow. The harvester defers it while its log may still change.
	Pending bool `json:"-"`
}

const (
	// MaxRawBytes bounds the source line a record keeps. The logs are
	// agent-writable and no cleanup removes the archive, so the harvester
	// drops longer lines and the strings over maxFieldBytes.
	MaxRawBytes   = 64 << 10
	maxFieldBytes = 4 << 10
)

// dropOversized clears the fields taken from a log that exceed their limit.
func (r *Record) dropOversized() {
	if len(r.Raw) > MaxRawBytes {
		r.Raw = nil
	}
	for _, field := range []*string{&r.Model, &r.Session, &r.Cwd, &r.GitBranch, &r.Version, &r.Effort} {
		if len(*field) > maxFieldBytes {
			*field = ""
		}
	}
}

// Tokens uses disjoint input buckets so they sum to the billed input.
// Reasoning is the part of Output spent on thinking.
type Tokens struct {
	Input      int64 `json:"input"`
	CacheWrite int64 `json:"cacheWrite"`
	CacheRead  int64 `json:"cacheRead"`
	Output     int64 `json:"output"`
	Reasoning  int64 `json:"reasoning"`
}

func (t Tokens) Total() int64 {
	return t.Input + t.CacheWrite + t.CacheRead + t.Output
}

func (t Tokens) IsZero() bool {
	return t == Tokens{}
}

func (t *Tokens) Add(other Tokens) {
	t.Input += other.Input
	t.CacheWrite += other.CacheWrite
	t.CacheRead += other.CacheRead
	t.Output += other.Output
	t.Reasoning += other.Reasoning
}

// Source extracts usage from one tool's session logs. Parse fills the
// tool-specific fields and the key, which must be unique for the tool and
// stable across forks and resumes that copy history into new logs. A copy
// must keep the original timestamp, or Parse must skip it. The harvester
// fills Tool and the project and store fields.
type Source interface {
	// Dirs lists the config-store-relative directories holding *.jsonl logs.
	Dirs() []string
	Parse(r io.Reader) ([]Record, error)
}

var (
	sourcesMu sync.RWMutex
	sources   = map[string]Source{}
)

func RegisterSource(tool string, source Source) {
	tool = strings.TrimSpace(tool)
	if tool == "" || source == nil {
		return
	}
	sourcesMu.Lock()
	sources[tool] = source
	sourcesMu.Unlock()
}

func sourceFor(tool string) (Source, bool) {
	sourcesMu.RLock()
	defer sourcesMu.RUnlock()
	source, ok := sources[tool]
	return source, ok
}

// Tools lists the tools with a registered source.
func Tools() []string {
	sourcesMu.RLock()
	defer sourcesMu.RUnlock()
	tools := make([]string, 0, len(sources))
	for tool := range sources {
		tools = append(tools, tool)
	}
	sort.Strings(tools)
	return tools
}
