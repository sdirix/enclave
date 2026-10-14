// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

//go:build !windows

package usage

import (
	"os"
	"syscall"
)

// logOpenFlags keeps a FIFO that an agent put in place of a log from blocking
// the host.
const logOpenFlags = os.O_RDONLY | syscall.O_NONBLOCK
