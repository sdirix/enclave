// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package usage

import (
	"fmt"
	"sort"
	"time"
)

const (
	GroupDay     = "day"
	GroupMonth   = "month"
	GroupModel   = "model"
	GroupProject = "project"
	GroupTool    = "tool"
)

// GroupByValues lists the supported groupings, the default first.
var GroupByValues = []string{GroupDay, GroupMonth, GroupModel, GroupProject, GroupTool}

type Sum struct {
	Requests int64  `json:"requests"`
	Tokens   Totals `json:"tokens"`
}

type Row struct {
	Key string `json:"key"`
	Sum
}

type Totals struct {
	Tokens
	Total int64 `json:"total"`
}

func (s *Sum) add(tokens Tokens) {
	s.Requests++
	s.Tokens.Add(tokens)
	s.Tokens.Total += tokens.Total()
}

// Summarize groups records. Day and month rows use loc and are sorted by
// date. Other rows are sorted by total tokens, largest first.
func Summarize(records []Record, groupBy string, loc *time.Location) (rows []Row, total Sum, err error) {
	keyOf, err := groupKey(groupBy, loc)
	if err != nil {
		return nil, Sum{}, err
	}
	index := map[string]int{}
	for _, record := range records {
		key := keyOf(record)
		i, ok := index[key]
		if !ok {
			i = len(rows)
			index[key] = i
			rows = append(rows, Row{Key: key})
		}
		rows[i].add(record.Tokens)
		total.add(record.Tokens)
	}
	if groupBy == GroupDay || groupBy == GroupMonth {
		sort.Slice(rows, func(i, j int) bool { return rows[i].Key < rows[j].Key })
	} else {
		sort.Slice(rows, func(i, j int) bool {
			if rows[i].Tokens.Total != rows[j].Tokens.Total {
				return rows[i].Tokens.Total > rows[j].Tokens.Total
			}
			return rows[i].Key < rows[j].Key
		})
	}
	return rows, total, nil
}

func groupKey(groupBy string, loc *time.Location) (func(Record) string, error) {
	if loc == nil {
		loc = time.Local
	}
	switch groupBy {
	case GroupDay:
		return func(r Record) string { return r.Timestamp.In(loc).Format(time.DateOnly) }, nil
	case GroupMonth:
		return func(r Record) string { return r.Timestamp.In(loc).Format(monthLayout) }, nil
	case GroupModel:
		return func(r Record) string { return orUnknown(r.Model) }, nil
	case GroupProject:
		return func(r Record) string {
			if r.Project != "" {
				return r.Project
			}
			return orUnknown(r.ProjectHash)
		}, nil
	case GroupTool:
		return func(r Record) string { return r.Tool }, nil
	}
	return nil, fmt.Errorf("unknown grouping %q", groupBy)
}

func orUnknown(value string) string {
	if value == "" {
		return "unknown"
	}
	return value
}
