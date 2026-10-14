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
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

const monthLayout = "2006-01"

var monthFilePattern = regexp.MustCompile(`^\d{4}-\d{2}\.jsonl$`)

// archive is the JSONL record store, one file per UTC month of the record
// timestamp. Sources keep the timestamp of a record copied into a later log
// or skip the copy, so deduplication only needs the keys of that one month.
type archive struct {
	dir  string
	keys map[string]map[string]struct{}
	// written lists the month files appended to since the last sync.
	written map[string]struct{}
}

func newArchive(dir string) *archive {
	return &archive{dir: dir, keys: map[string]map[string]struct{}{}, written: map[string]struct{}{}}
}

func (a *archive) path(month string) string {
	return filepath.Join(a.dir, month+".jsonl")
}

// add appends the records whose keys the archive does not hold yet and returns
// how many it appended.
func (a *archive) add(records []Record) (int, error) {
	byMonth := map[string][]Record{}
	for _, record := range records {
		month := record.Timestamp.UTC().Format(monthLayout)
		keys, err := a.monthKeys(month)
		if err != nil {
			return 0, err
		}
		if _, ok := keys[record.Key]; ok {
			continue
		}
		keys[record.Key] = struct{}{}
		byMonth[month] = append(byMonth[month], record)
	}
	added := 0
	for month, batch := range byMonth {
		if err := a.append(month, batch); err != nil {
			// The keys stay marked so a retry in this run cannot duplicate the
			// part of the batch that may have been written. The next run
			// reloads them from disk.
			return added, err
		}
		added += len(batch)
	}
	return added, nil
}

func (a *archive) monthKeys(month string) (map[string]struct{}, error) {
	if keys, ok := a.keys[month]; ok {
		return keys, nil
	}
	keys := map[string]struct{}{}
	file, err := os.Open(a.path(month))
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return nil, err
	default:
		defer func() { _ = file.Close() }()
		err = ScanLines(file, func(line []byte) {
			if key := lineKey(line); key != "" {
				keys[key] = struct{}{}
			}
		})
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", a.path(month), err)
		}
	}
	a.keys[month] = keys
	return keys, nil
}

var keyPrefix = []byte(`{"key":"`)

// lineKey reads the key from the prefix the archive writes, which avoids
// decoding every record. Lines edited by hand fall back to a full decode.
func lineKey(line []byte) string {
	if rest, ok := bytes.CutPrefix(line, keyPrefix); ok {
		if end := bytes.IndexByte(rest, '"'); end > 0 && bytes.IndexByte(rest[:end], '\\') < 0 {
			return string(rest[:end])
		}
	}
	var record struct {
		Key string `json:"key"`
	}
	if json.Unmarshal(line, &record) != nil {
		return ""
	}
	return record.Key
}

func (a *archive) append(month string, records []Record) error {
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	for _, record := range records {
		if err := encoder.Encode(record); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(a.dir, 0o700); err != nil {
		return err
	}
	path := a.path(month)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o600) // #nosec G304 -- path is resolved from enclave's own state directory.
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	if err := truncateTornLine(file); err != nil {
		return fmt.Errorf("repair %s: %w", path, err)
	}
	a.written[path] = struct{}{}
	if _, err := file.Write(buf.Bytes()); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// truncateTornLine drops a partial last line that an interrupted append left.
// Terminating it instead would turn it into a line whose key lineKey reads,
// hiding a record that was never written whole.
func truncateTornLine(file *os.File) error {
	info, err := file.Stat()
	if err != nil {
		return err
	}
	end := info.Size()
	chunk := make([]byte, 64<<10)
	for offset := end; offset > 0; {
		n := min(int64(len(chunk)), offset)
		offset -= n
		if _, err := file.ReadAt(chunk[:n], offset); err != nil {
			return err
		}
		if i := bytes.LastIndexByte(chunk[:n], '\n'); i >= 0 {
			if keep := offset + int64(i) + 1; keep < end {
				return file.Truncate(keep)
			}
			return nil
		}
	}
	if end > 0 {
		return file.Truncate(0)
	}
	return nil
}

// sync flushes the month files written since the last sync.
func (a *archive) sync() error {
	for path := range a.written {
		file, err := os.OpenFile(path, os.O_RDWR, 0) // #nosec G304 -- path is resolved from enclave's own state directory.
		if err != nil {
			return err
		}
		err = file.Sync()
		_ = file.Close()
		if err != nil {
			return fmt.Errorf("sync %s: %w", path, err)
		}
		delete(a.written, path)
	}
	return nil
}

// Filter selects archived records. Zero bounds are open.
type Filter struct {
	Since time.Time
	Until time.Time
	Tool  string
}

func (f Filter) matches(record Record) bool {
	if !f.Since.IsZero() && record.Timestamp.Before(f.Since) {
		return false
	}
	if !f.Until.IsZero() && !record.Timestamp.Before(f.Until) {
		return false
	}
	return f.Tool == "" || record.Tool == f.Tool
}

// coversMonth reports whether any instant of the UTC month can match.
func (f Filter) coversMonth(month time.Time) bool {
	next := month.AddDate(0, 1, 0)
	if !f.Since.IsZero() && !next.After(f.Since) {
		return false
	}
	return f.Until.IsZero() || month.Before(f.Until)
}

// Load reads the archived records that match filter, without their raw source
// lines. Malformed lines are counted and skipped.
func Load(dir string, filter Filter) (records []Record, skipped int, err error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, 0, nil
	}
	if err != nil {
		return nil, 0, err
	}
	for _, entry := range entries {
		name := entry.Name()
		if !entry.Type().IsRegular() || !monthFilePattern.MatchString(name) {
			continue
		}
		month, err := time.Parse(monthLayout, strings.TrimSuffix(name, ".jsonl"))
		if err != nil || !filter.coversMonth(month) {
			continue
		}
		file, err := os.Open(filepath.Join(dir, name)) // #nosec G304 -- path is resolved from enclave's own state directory.
		if err != nil {
			return nil, 0, err
		}
		err = ScanLines(file, func(line []byte) {
			if len(bytes.TrimSpace(line)) == 0 {
				return
			}
			var record Record
			if json.Unmarshal(line, &record) != nil || record.Key == "" {
				skipped++
				return
			}
			record.Raw = nil
			if filter.matches(record) {
				records = append(records, record)
			}
		})
		_ = file.Close()
		if err != nil {
			return nil, 0, fmt.Errorf("read %s: %w", name, err)
		}
	}
	sort.SliceStable(records, func(i, j int) bool {
		return records[i].Timestamp.Before(records[j].Timestamp)
	})
	return records, skipped, nil
}
