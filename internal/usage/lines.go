// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package usage

import (
	"bufio"
	"errors"
	"io"
)

// MaxLineBytes bounds one log line. Longer lines are skipped: usage lines are
// small, and the logs are agent-writable.
const MaxLineBytes = 16 << 20

// ScanLines calls fn for every newline-terminated line, without the newline.
// The slice is only valid during the call. A final line without a newline is
// skipped because its writer may still be appending to it.
func ScanLines(r io.Reader, fn func(line []byte)) error {
	reader := bufio.NewReaderSize(r, 64<<10)
	var long []byte
	oversized := false
	for {
		chunk, err := reader.ReadSlice('\n')
		switch {
		case errors.Is(err, bufio.ErrBufferFull):
			if !oversized && len(long)+len(chunk) <= MaxLineBytes {
				long = append(long, chunk...)
			} else {
				oversized = true
				long = long[:0]
			}
			continue
		case errors.Is(err, io.EOF):
			return nil
		case err != nil:
			return err
		}
		switch {
		case oversized:
			oversized = false
		case len(long) == 0:
			fn(chunk[:len(chunk)-1])
		case len(long)+len(chunk) <= MaxLineBytes:
			long = append(long, chunk...)
			fn(long[:len(long)-1])
		}
		long = long[:0]
	}
}
