package tools

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestTruncateTailKeepsLastLines(t *testing.T) {
	got, info := truncateTail("one\ntwo\nthree\n", 2, 100)
	if got != "two\nthree\n" {
		t.Fatalf("got %q, want last two lines", got)
	}
	if !info.truncated || info.reason != "line limit" || info.shown != 2 || info.total != 3 {
		t.Fatalf("truncation info = %#v", info)
	}
}

func TestTruncateTailKeepsUTF8TailOfLongLine(t *testing.T) {
	input := strings.Repeat("界", 20)
	got, info := truncateTail(input, 10, len("界")*3)
	if !info.truncated || info.reason != "byte limit" {
		t.Fatalf("truncation info = %#v", info)
	}
	if len(got) > len("界")*3 || !utf8.ValidString(got) || !strings.HasSuffix(input, got) {
		t.Fatalf("tail = %q, not a valid suffix within the byte limit", got)
	}
}
