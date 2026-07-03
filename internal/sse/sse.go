package sse

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"strings"
)

// Event represents a single Server-Sent Event parsed from a stream.
type Event struct {
	Type string // the "event:" field; empty if none was set
	Data string // the "data:" field(s) joined by "\n"
}

// Decode reads SSE events from r and sends them on the returned channel.
// The channel is closed when r is exhausted or ctx is cancelled.
// Parsing follows the W3C Server-Sent Events specification.
func Decode(ctx context.Context, r io.Reader) <-chan Event {
	ch := make(chan Event, 1)
	go func() {
		defer close(ch)
		scanner := bufio.NewScanner(r)
		scanner.Split(scanLines)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

		var eventType string
		var dataLines []string

		flush := func() {
			if len(dataLines) == 0 && eventType == "" {
				return
			}
			if len(dataLines) > 0 {
				evt := Event{
					Type: eventType,
					Data: strings.Join(dataLines, "\n"),
				}
				select {
				case ch <- evt:
				case <-ctx.Done():
					return
				}
			}
			eventType = ""
			dataLines = nil
		}

		for scanner.Scan() {
			select {
			case <-ctx.Done():
				return
			default:
			}

			line := scanner.Text()

			if line == "" {
				flush()
				continue
			}

			if line[0] == ':' {
				continue
			}

			field, value := parseField(line)
			switch field {
			case "event":
				eventType = value
			case "data":
				dataLines = append(dataLines, value)
			}
		}

		flush()
	}()
	return ch
}

// parseField splits an SSE line into field name and value.
// Per the spec, if the line contains a colon, the field is everything before it
// and the value is everything after it (with one leading space stripped).
// If there is no colon, the entire line is the field name and the value is empty.
func parseField(line string) (field, value string) {
	i := strings.IndexByte(line, ':')
	if i < 0 {
		return line, ""
	}
	field = line[:i]
	value = line[i+1:]
	if len(value) > 0 && value[0] == ' ' {
		value = value[1:]
	}
	return field, value
}

// scanLines is a bufio.SplitFunc that splits on \n, \r\n, or bare \r.
// This is necessary because the SSE spec allows all three line ending forms.
func scanLines(data []byte, atEOF bool) (advance int, token []byte, err error) {
	if atEOF && len(data) == 0 {
		return 0, nil, nil
	}

	if i := bytes.IndexAny(data, "\r\n"); i >= 0 {
		if data[i] == '\r' {
			if i+1 < len(data) && data[i+1] == '\n' {
				return i + 2, data[:i], nil
			}
			return i + 1, data[:i], nil
		}
		return i + 1, data[:i], nil
	}

	if atEOF {
		return len(data), data, nil
	}

	return 0, nil, nil
}
