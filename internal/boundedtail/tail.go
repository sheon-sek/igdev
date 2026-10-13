// Package boundedtail retains recent diagnostic lines within fixed byte budgets.
package boundedtail

import (
	"bytes"
	"strings"
	"sync"
)

// Tail is an io.Writer safe for concurrent writers. Oversized lines retain their
// suffix, without a marker. Writes always consume the full input.
type Tail struct {
	mu                  sync.Mutex
	lines               []string
	head, count, stored int
	budget              int
	partial             []byte
	start, size         int
}

// New reserves maxLineBytes for the current line and the remaining maxBytes
// for completed lines. All limits must be positive and maxLineBytes <= maxBytes.
// Storage never grows with the input size; line metadata is bounded by maxLines.
func New(maxLines, maxBytes, maxLineBytes int) *Tail {
	if maxLines <= 0 || maxBytes <= 0 || maxLineBytes <= 0 || maxLineBytes > maxBytes {
		panic("boundedtail: invalid limits")
	}
	return &Tail{lines: make([]string, maxLines), partial: make([]byte, maxLineBytes), budget: maxBytes - maxLineBytes}
}

func (t *Tail) drop() {
	t.stored -= len(t.lines[t.head])
	t.lines[t.head] = ""
	t.head = (t.head + 1) % len(t.lines)
	t.count--
}

func (t *Tail) appendBytes(p []byte) {
	if len(p) >= len(t.partial) {
		p = p[len(p)-len(t.partial):]
		t.start, t.size = 0, 0
	}
	if excess := t.size + len(p) - len(t.partial); excess > 0 {
		t.start = (t.start + excess) % len(t.partial)
		t.size -= excess
	}
	end := (t.start + t.size) % len(t.partial)
	n := copy(t.partial[end:], p)
	copy(t.partial, p[n:])
	t.size += len(p)
}

func (t *Tail) current() string {
	p := make([]byte, t.size)
	n := copy(p, t.partial[t.start:min(t.start+t.size, len(t.partial))])
	copy(p[n:], t.partial[:t.size-n])
	return strings.TrimSuffix(string(p), "\r")
}

// Write scans input once and copies only the bounded suffix of each chunk.
func (t *Tail) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	n := len(p)
	for len(p) > 0 {
		end := bytes.IndexByte(p, '\n')
		if end < 0 {
			t.appendBytes(p)
			break
		}
		t.appendBytes(p[:end])
		line := t.current()
		// Clone after trimming so retained strings own exactly their bytes.
		if len(line) > t.budget {
			line = line[len(line)-t.budget:]
		}
		line = strings.Clone(line)
		for t.count > 0 && (t.count == len(t.lines) || t.stored+len(line) > t.budget) {
			t.drop()
		}
		t.lines[(t.head+t.count)%len(t.lines)] = line
		t.count++
		t.stored += len(line)
		t.start, t.size = 0, 0
		p = p[end+1:]
	}
	return n, nil
}

// Lines returns an independent snapshot, oldest first, including an unterminated
// final line. Empty output is a non-nil slice. A trailing CR is removed as in CRLF.
func (t *Tail) Lines() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]string, 0, len(t.lines))
	skip := 0
	if t.size > 0 && t.count == len(t.lines) {
		skip = 1
	}
	for i := skip; i < t.count; i++ {
		out = append(out, t.lines[(t.head+i)%len(t.lines)])
	}
	if t.size > 0 {
		out = append(out, t.current())
	}
	return out
}
