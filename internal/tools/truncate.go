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

// truncateTail keeps complete UTF-8 lines from the end of text. If the final
// line itself is larger than maxBytes, the helper keeps the last complete
// UTF-8 byte sequence that fits. This is useful for command output where the
// most actionable information is usually near the end (for example, a test
// failure summary).
func truncateTail(text string, maxLines, maxBytes int) (string, truncation) {
	if text == "" {
		return text, truncation{}
	}
	if !utf8.ValidString(text) {
		return "", truncation{truncated: true, reason: "invalid UTF-8"}
	}
	if maxLines <= 0 || maxBytes <= 0 {
		return "", truncation{truncated: true, reason: "byte limit", total: logicalLineCount(text)}
	}

	lines := strings.SplitAfter(text, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	total := len(lines)
	if total <= maxLines && len(text) <= maxBytes {
		return text, truncation{total: total, shown: total}
	}

	selected := make([]string, 0, minInt(total, maxLines))
	usedBytes := 0
	reason := "line limit"
	for i := total - 1; i >= 0 && len(selected) < maxLines; i-- {
		line := lines[i]
		lineBytes := len(line)
		if usedBytes+lineBytes > maxBytes {
			reason = "byte limit"
			if len(selected) == 0 {
				line = takeUTF8Tail(line, maxBytes)
				if line != "" {
					selected = append(selected, line)
					usedBytes = len(line)
				}
			}
			break
		}
		selected = append(selected, line)
		usedBytes += lineBytes
	}

	// We collected backwards so restore the original order of the retained
	// suffix. Each line already carries its original newline terminator.
	for left, right := 0, len(selected)-1; left < right; left, right = left+1, right-1 {
		selected[left], selected[right] = selected[right], selected[left]
	}
	return strings.Join(selected, ""), truncation{
		truncated: true,
		reason:    reason,
		shown:     len(selected),
		total:     total,
	}
}

func takeUTF8Tail(text string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	start := len(text)
	used := 0
	for start > 0 {
		runeValue, size := utf8.DecodeLastRuneInString(text[:start])
		if runeValue == utf8.RuneError && size == 0 {
			break
		}
		if used+size > maxBytes {
			break
		}
		start -= size
		used += size
	}
	return text[start:]
}

func logicalLineCount(text string) int {
	if text == "" {
		return 0
	}
	lines := strings.SplitAfter(text, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return len(lines)
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
