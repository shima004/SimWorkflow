package worker

import (
	"bytes"
	"io"
)

// scanLogBackwards visits lines from newest to oldest until visit returns true.
// Reading in blocks avoids draining large DEBUG logs; oversized lines are
// skipped to bound memory. completeOnly ignores an unterminated final line.
func scanLogBackwards(r io.ReaderAt, size int64, completeOnly bool, visit func([]byte) bool) {
	const chunkSize = 64 << 10
	const maxLine = 4 << 20
	var pending []byte
	discard := completeOnly
	for end := size; end > 0; {
		start := max(int64(0), end-chunkSize)
		chunk := make([]byte, end-start, int(end-start)+len(pending))
		if _, err := r.ReadAt(chunk, start); err != nil {
			return
		}
		data := append(chunk, pending...)
		last := len(data)
		for {
			i := bytes.LastIndexByte(data[:last], '\n')
			if i < 0 {
				break
			}
			if !discard && last-i-1 <= maxLine && visit(data[i+1:last]) {
				return
			}
			discard = false
			last = i
		}
		if last > maxLine {
			discard = true
		}
		pending = nil
		if !discard {
			pending = append(pending, data[:last]...)
		}
		end = start
	}
	if !discard {
		visit(pending)
	}
}
