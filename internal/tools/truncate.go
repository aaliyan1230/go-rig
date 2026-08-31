package tools

import (
	"strings"
	"unicode/utf8"
)

const (
	DefaultReadMaxLines = 2000
	DefaultReadMaxBytes = 50 * 1024
)

type truncation struct {
	truncated bool
	reason    string
	shown     int
	total     int
}

// truncateHead keeps complete UTF-8 lines from the start of text. The input is
// already known to be valid UTF-8; the validity check makes the helper safe to
// reuse independently too.
func truncateHead(text string, maxLines, maxBytes int) (string, truncation) {
	if text == "" {
		return text, truncation{}
	}
	if !utf8.ValidString(text) {
		return "", truncation{truncated: true, reason: "invalid UTF-8"}
	}

	lines := strings.SplitAfter(text, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}

	if len(lines) <= maxLines && len(text) <= maxBytes {
		return text, truncation{total: len(lines), shown: len(lines)}
	}

	var out strings.Builder
	for i, line := range lines {
		if i >= maxLines {
			return out.String(), truncation{truncated: true, reason: "line limit", shown: i, total: len(lines)}
		}
		if out.Len()+len(line) > maxBytes {
			return out.String(), truncation{truncated: true, reason: "byte limit", shown: i, total: len(lines)}
		}
		out.WriteString(line)
	}

	return out.String(), truncation{total: len(lines), shown: len(lines)}
}
