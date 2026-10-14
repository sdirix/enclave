// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package cli

import (
	"fmt"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"enclave/internal/usage"
)

func usageCommand(res *Result) *cobra.Command {
	view := &res.UsageView

	cmd := &cobra.Command{
		Use:   "usage",
		Short: "Show token usage across all sessions",
		Long: `Show model token usage of all sessions on this host.

Archives new usage from every config store first, so the report includes
running sessions and stores removed by cleanup since they were archived.`,
		Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			view.By = strings.ToLower(strings.TrimSpace(view.By))
			if !slices.Contains(usage.GroupByValues, view.By) {
				return fmt.Errorf("invalid --by %q (expected %s)", view.By, strings.Join(usage.GroupByValues, "|"))
			}
			res.Action = "usage"
			return nil
		},
	}

	cmd.Flags().StringVar(&view.By, "by", usage.GroupDay, "Group rows by "+strings.Join(usage.GroupByValues, "|"))
	cmd.Flags().StringVar(&view.Since, "since", "", "Only usage since a date (2026-10-01), an RFC3339 timestamp, or a duration (12h, 30d)")
	cmd.Flags().StringVar(&view.Until, "until", "", "Only usage until a date (inclusive), an RFC3339 timestamp, or a duration ago")
	cmd.Flags().BoolVar(&view.JSON, "json", false, "Emit JSON instead of the table")
	addOptionFlagsByName(cmd.Flags(), &res.Options, &res.Sources, "tool")
	return cmd
}
