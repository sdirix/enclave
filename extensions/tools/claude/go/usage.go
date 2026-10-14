// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package claude

import (
	"bytes"
	"encoding/json"
	"io"
	"time"

	"enclave/internal/usage"
)

func init() {
	usage.RegisterSource("claude", usageSource{})
}

// usageSource reads the session transcripts, including subagent transcripts
// in <session>/subagents/.
type usageSource struct{}

func (usageSource) Dirs() []string { return []string{"projects"} }

type transcriptLine struct {
	Type      string    `json:"type"`
	UUID      string    `json:"uuid"`
	Timestamp time.Time `json:"timestamp"`
	RequestID string    `json:"requestId"`
	SessionID string    `json:"sessionId"`
	Cwd       string    `json:"cwd"`
	GitBranch string    `json:"gitBranch"`
	Version   string    `json:"version"`
	Effort    string    `json:"effort"`
	Message   *struct {
		ID         string           `json:"id"`
		Model      string           `json:"model"`
		StopReason *string          `json:"stop_reason"`
		Usage      *transcriptUsage `json:"usage"`
	} `json:"message"`
}

type transcriptUsage struct {
	usageCounts
	OutputTokensDetails struct {
		ThinkingTokens int64 `json:"thinking_tokens"`
	} `json:"output_tokens_details"`
	Iterations []usageCounts `json:"iterations"`
}

type usageCounts struct {
	InputTokens              int64 `json:"input_tokens"`
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
	OutputTokens             int64 `json:"output_tokens"`
}

// tokens sums usage.iterations when present. The top-level counts cover only
// the attempt that produced the message, not compaction or refusal fallback
// attempts.
func (u transcriptUsage) tokens() usage.Tokens {
	counts := u.usageCounts
	if len(u.Iterations) > 0 {
		counts = usageCounts{}
		for _, iteration := range u.Iterations {
			counts.InputTokens += iteration.InputTokens
			counts.CacheCreationInputTokens += iteration.CacheCreationInputTokens
			counts.CacheReadInputTokens += iteration.CacheReadInputTokens
			counts.OutputTokens += iteration.OutputTokens
		}
	}
	return usage.Tokens{
		Input:      counts.InputTokens,
		CacheWrite: counts.CacheCreationInputTokens,
		CacheRead:  counts.CacheReadInputTokens,
		Output:     counts.OutputTokens,
		Reasoning:  u.OutputTokensDetails.ThinkingTokens,
	}
}

var usageMarker = []byte(`"usage"`)

// Parse keys records by message and request ID. Claude writes one line per
// content block, and forked sessions copy lines into a new transcript, so both
// repeat the key. Lines written while the response streams carry partial
// output counts, so the largest count wins. Responses in a transcript are
// sequential: only the last one can still be streaming.
func (usageSource) Parse(r io.Reader) ([]usage.Record, error) {
	var (
		records []usage.Record
		lines   [][]byte
		index   = map[string]int{}
		stopped = map[string]bool{}
		lastKey string
	)
	err := usage.ScanLines(r, func(line []byte) {
		if !bytes.Contains(line, usageMarker) {
			return
		}
		var entry transcriptLine
		if json.Unmarshal(line, &entry) != nil || entry.Type != "assistant" {
			return
		}
		message := entry.Message
		if message == nil || message.Usage == nil || message.Model == "<synthetic>" {
			return
		}
		key := message.ID
		if entry.RequestID != "" {
			key += ":" + entry.RequestID
		}
		if message.ID == "" {
			key = entry.UUID
		}
		tokens := message.Usage.tokens()
		if tokens.IsZero() {
			return
		}
		lastKey = key
		if message.StopReason != nil {
			stopped[key] = true
		}
		i, seen := index[key]
		if seen && tokens.Output < records[i].Tokens.Output {
			return
		}
		record := usage.Record{
			Key:       key,
			Timestamp: entry.Timestamp,
			Model:     message.Model,
			Session:   entry.SessionID,
			Cwd:       entry.Cwd,
			GitBranch: entry.GitBranch,
			Version:   entry.Version,
			Effort:    entry.Effort,
			Tokens:    tokens,
		}
		if !seen {
			i = len(records)
			index[key] = i
			records = append(records, usage.Record{})
			lines = append(lines, nil)
		}
		records[i] = record
		if len(line) <= usage.MaxRawBytes {
			lines[i] = append(lines[i][:0], line...)
			return
		}
		// Content makes up most of a long line, so it is stripped right away
		// instead of being held until the end. A failure drops the record
		// below, like any other line that cannot be stripped.
		lines[i], _ = withoutContent(line)
	})
	if lastKey != "" && !stopped[lastKey] {
		records[index[lastKey]].Pending = true
	}
	// Stripping the content is the expensive part, so only the line each
	// record keeps pays for it.
	kept := records[:0]
	for i, record := range records {
		raw, err := withoutContent(lines[i])
		if err != nil {
			continue
		}
		record.Raw = raw
		kept = append(kept, record)
	}
	return kept, err
}

// withoutContent drops message.content, which holds the conversation, and
// keeps every other field of the line.
func withoutContent(line []byte) (json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(line, &fields); err != nil {
		return nil, err
	}
	var message map[string]json.RawMessage
	if err := json.Unmarshal(fields["message"], &message); err != nil {
		return nil, err
	}
	delete(message, "content")
	encoded, err := marshalRaw(message)
	if err != nil {
		return nil, err
	}
	fields["message"] = encoded
	return marshalRaw(fields)
}

func marshalRaw(value any) (json.RawMessage, error) {
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte{'\n'}), nil
}
