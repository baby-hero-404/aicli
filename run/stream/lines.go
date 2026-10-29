package stream

import "bytes"

// DefaultMaxLine is the default cap, in bytes, on a single output line.
const DefaultMaxLine = 1 << 20

// LineWriter is an io.Writer that splits what is written to it into lines and calls fn with
// each complete one, without the line terminator ("\n" or "\r\n"). Empty lines are skipped.
// A line longer than the cap is dropped in full (up to its terminator) and counted, so a
// runaway line cannot exhaust memory. It is not safe for concurrent use.
type LineWriter struct {
	fn      func(line []byte)
	max     int
	buf     []byte
	dropped int
	skip    bool // inside an over-long line, discarding until the next newline
}

// NewLineWriter returns a LineWriter that calls fn for every line. A max below 1 means
// DefaultMaxLine. The slice passed to fn is only valid during the call.
func NewLineWriter(max int, fn func(line []byte)) *LineWriter {
	if max < 1 {
		max = DefaultMaxLine
	}
	return &LineWriter{fn: fn, max: max}
}

// Write implements io.Writer. It never returns an error.
func (w *LineWriter) Write(p []byte) (int, error) {
	n := len(p)
	for len(p) > 0 {
		i := bytes.IndexByte(p, '\n')
		if i < 0 {
			w.accumulate(p)
			break
		}
		w.accumulate(p[:i])
		w.emit()
		p = p[i+1:]
	}
	return n, nil
}

// Flush emits a final unterminated line, if any. Call it when the output ends.
func (w *LineWriter) Flush() { w.emit() }

// Dropped reports how many over-long lines were discarded.
func (w *LineWriter) Dropped() int { return w.dropped }

func (w *LineWriter) accumulate(p []byte) {
	if w.skip || len(p) == 0 {
		return
	}
	if len(w.buf)+len(p) > w.max {
		w.skip = true
		w.buf = w.buf[:0]
		w.dropped++
		return
	}
	w.buf = append(w.buf, p...)
}

func (w *LineWriter) emit() {
	if w.skip {
		w.skip = false
		return
	}
	line := bytes.TrimSuffix(w.buf, []byte("\r"))
	if len(line) > 0 {
		w.fn(line)
	}
	w.buf = w.buf[:0]
}
