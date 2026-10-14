// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package util

import (
	"strings"
	"unicode"
)

// DisplaySafe replaces every non-printable rune with '?', so agent-controlled
// text cannot carry escape sequences into a terminal. Replacing rather than
// dropping keeps column widths honest, since padding counts runes.
func DisplaySafe(value string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsPrint(r) {
			return r
		}
		return '?'
	}, value)
}
