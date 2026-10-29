package stream

import (
	"reflect"
	"strings"
	"testing"
)

func TestTruncate(t *testing.T) {
	tests := []struct {
		name string
		in   string
		max  int
		want string
	}{
		{"short unchanged", "hello", 10, "hello"},
		{"exact length unchanged", "hello", 5, "hello"},
		{"cut with ellipsis", "hello world", 5, "hello…"},
		{"does not split a multibyte rune", "ééé", 3, "é…"},
		{"zero max uses default", "hi", 0, "hi"},
		{"negative max uses default", strings.Repeat("a", DefaultMaxSummary+1), -1, strings.Repeat("a", DefaultMaxSummary) + "…"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Truncate(tt.in, tt.max); got != tt.want {
				t.Errorf("Truncate(%q, %d) = %q, want %q", tt.in, tt.max, got, tt.want)
			}
		})
	}
}

func TestLineWriter(t *testing.T) {
	tests := []struct {
		name    string
		max     int
		writes  []string
		flush   bool
		want    []string
		dropped int
	}{
		{"one line per write", 0, []string{"a\n", "b\n"}, false, []string{"a", "b"}, 0},
		{"line split across writes", 0, []string{"hel", "lo\nwor", "ld\n"}, false, []string{"hello", "world"}, 0},
		{"several lines in one write", 0, []string{"a\nb\nc\n"}, false, []string{"a", "b", "c"}, 0},
		{"crlf terminator", 0, []string{"a\r\nb\r\n"}, false, []string{"a", "b"}, 0},
		{"empty lines skipped", 0, []string{"\n\na\n\n"}, false, []string{"a"}, 0},
		{"unterminated tail needs flush", 0, []string{"a\nb"}, false, []string{"a"}, 0},
		{"flush emits unterminated tail", 0, []string{"a\nb"}, true, []string{"a", "b"}, 0},
		{"over-long line dropped, next kept", 4, []string{"toolong\nok\n"}, false, []string{"ok"}, 1},
		{"over-long line dropped across writes", 4, []string{"too", "long", "er\nok\n"}, false, []string{"ok"}, 1},
		{"line exactly at the cap kept", 4, []string{"abcd\n"}, false, []string{"abcd"}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got []string
			w := NewLineWriter(tt.max, func(line []byte) { got = append(got, string(line)) })
			for _, s := range tt.writes {
				n, err := w.Write([]byte(s))
				if err != nil || n != len(s) {
					t.Fatalf("Write(%q) = %d, %v", s, n, err)
				}
			}
			if tt.flush {
				w.Flush()
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("lines = %q, want %q", got, tt.want)
			}
			if w.Dropped() != tt.dropped {
				t.Errorf("Dropped() = %d, want %d", w.Dropped(), tt.dropped)
			}
		})
	}
}
