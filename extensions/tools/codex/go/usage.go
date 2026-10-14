// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package codex

import (
	"encoding/json"
	"fmt"
	"io"
	"time"

	"enclave/internal/usage"
)

func init() {
	usage.RegisterSource("codex", usageSource{})
}

// usageSource reads the session rollouts. Codex moves archived sessions out
// of sessions/, so both directories count.
type usageSource struct{}

func (usageSource) Dirs() []string { return []string{"sessions", "archived_sessions"} }

type rolloutLine struct {
	Timestamp time.Time       `json:"timestamp"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
}

type sessionMeta struct {
	ID         string `json:"id"`
	SessionID  string `json:"session_id"`
	Cwd        string `json:"cwd"`
	CLIVersion string `json:"cli_version"`
	Git        *struct {
		Branch string `json:"branch"`
	} `json:"git"`
}

type turnContext struct {
	TurnID string          `json:"turn_id"`
	Cwd    string          `json:"cwd"`
	Model  string          `json:"model"`
	Effort json.RawMessage `json:"effort"`
}

type tokenUsage struct {
	InputTokens           int64 `json:"input_tokens"`
	CachedInputTokens     int64 `json:"cached_input_tokens"`
	CacheWriteInputTokens int64 `json:"cache_write_input_tokens"`
	OutputTokens          int64 `json:"output_tokens"`
	ReasoningOutputTokens int64 `json:"reasoning_output_tokens"`
	TotalTokens           int64 `json:"total_tokens"`
}

// tokens converts to disjoint buckets. Both cache counts are part of
// input_tokens, as in the Responses API.
func (u tokenUsage) tokens() usage.Tokens {
	return usage.Tokens{
		Input:      max(u.InputTokens-u.CachedInputTokens-u.CacheWriteInputTokens, 0),
		CacheWrite: u.CacheWriteInputTokens,
		CacheRead:  u.CachedInputTokens,
		Output:     u.OutputTokens,
		Reasoning:  u.ReasoningOutputTokens,
	}
}

type tokenUsageRecord struct {
	ThreadID   string     `json:"thread_id"`
	TurnID     string     `json:"turn_id"`
	SessionID  string     `json:"session_id"`
	ResponseID string     `json:"response_id"`
	Usage      tokenUsage `json:"usage"`
}

type tokenCountEvent struct {
	Type string `json:"type"`
	Info *struct {
		TotalTokenUsage tokenUsage `json:"total_token_usage"`
		LastTokenUsage  tokenUsage `json:"last_token_usage"`
	} `json:"info"`
}

// Parse prefers the per-response token_usage_record items, keyed by response
// ID. Rollouts from Codex versions without them fall back to token_count
// events, which repeat when only rate limits change and are therefore keyed
// by the cumulative thread totals.
func (usageSource) Parse(r io.Reader) ([]usage.Record, error) {
	var (
		meta     sessionMeta
		metaSeen bool
		// metaID is the latest session_meta's thread. Events after a
		// parent's entry in a fork are copies of that parent's.
		metaID     string
		sawRecords bool
		current    turnContext
		turns      = map[string]turnContext{}
		records    []usage.Record
		fallback   []usage.Record
		lastTotal  tokenUsage
	)
	record := func(line []byte, ts time.Time, turn turnContext, session string, tokens usage.Tokens) usage.Record {
		cwd := turn.Cwd
		if cwd == "" {
			cwd = meta.Cwd
		}
		branch := ""
		if meta.Git != nil {
			branch = meta.Git.Branch
		}
		return usage.Record{
			Timestamp: ts,
			Model:     turn.Model,
			Session:   session,
			Cwd:       cwd,
			GitBranch: branch,
			Version:   meta.CLIVersion,
			Effort:    effortString(turn.Effort),
			Tokens:    tokens,
			Raw:       append(json.RawMessage(nil), line...),
		}
	}
	err := usage.ScanLines(r, func(line []byte) {
		var entry rolloutLine
		if json.Unmarshal(line, &entry) != nil {
			return
		}
		switch entry.Type {
		case "session_meta":
			// The first entry describes this rollout. A fork can carry its
			// parent's entry after it.
			var latest sessionMeta
			if json.Unmarshal(entry.Payload, &latest) != nil {
				return
			}
			metaID = latest.ID
			if !metaSeen {
				meta, metaSeen = latest, true
			}
		case "turn_context":
			var turn turnContext
			if json.Unmarshal(entry.Payload, &turn) != nil {
				return
			}
			current = turn
			if turn.TurnID != "" {
				turns[turn.TurnID] = turn
			}
		case "token_usage_record":
			var payload tokenUsageRecord
			if json.Unmarshal(entry.Payload, &payload) != nil || payload.ResponseID == "" {
				return
			}
			sawRecords = true
			// A fork copies its parent's records with their original
			// thread. They are archived from the parent's rollout, and a
			// copy's new timestamp could put it in another archive month.
			if payload.ThreadID != "" && meta.ID != "" && payload.ThreadID != meta.ID {
				return
			}
			turn, ok := turns[payload.TurnID]
			if !ok {
				turn = current
			}
			session := payload.SessionID
			if session == "" {
				session = payload.ThreadID
			}
			rec := record(line, entry.Timestamp, turn, session, payload.Usage.tokens())
			rec.Key = payload.ResponseID
			records = append(records, rec)
		case "event_msg":
			var event tokenCountEvent
			if json.Unmarshal(entry.Payload, &event) != nil || event.Type != "token_count" || event.Info == nil {
				return
			}
			total := event.Info.TotalTokenUsage
			tokens := event.Info.LastTokenUsage.tokens()
			if total == lastTotal || tokens.IsZero() {
				return
			}
			lastTotal = total
			session := meta.SessionID
			if session == "" {
				session = meta.ID
			}
			rec := record(line, entry.Timestamp, current, session, tokens)
			rec.Key = fmt.Sprintf("%s:%d:%d:%d:%d:%d", metaID, total.InputTokens, total.CachedInputTokens,
				total.OutputTokens, total.ReasoningOutputTokens, total.TotalTokens)
			fallback = append(fallback, rec)
		}
	})
	if sawRecords {
		return records, err
	}
	return fallback, err
}

func effortString(raw json.RawMessage) string {
	var effort string
	if json.Unmarshal(raw, &effort) != nil {
		return ""
	}
	return effort
}
