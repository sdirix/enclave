// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package runtime

import (
	"enclave/internal/backend"
	"enclave/internal/logx"
	"enclave/internal/usage"
)

// archiveUsage copies token usage from a config store into the host usage
// archive, which outlives the store. Before a launch it also saves logs the
// tool would expire on startup. A store about to be removed is archived in
// full. A persistent one may be shared with a running session, so its
// unfinished responses and a busy archive are left for a later harvest.
func (r *Runtime) archiveUsage(store *backend.StoreRef, removing bool) {
	if store == nil || r.host.Home == "" {
		return
	}
	_, err := usage.Harvest(r.host.Home, []usage.Store{{
		Tool:        store.Key.Owner,
		ProjectHash: store.Key.ProjectHash,
		Key:         store.Key.Suffix,
	}}, usage.Options{Removing: removing, Wait: removing, Notify: logx.Infof})
	if err != nil {
		logx.Warnf("Failed to archive token usage: %v", err)
	}
}
