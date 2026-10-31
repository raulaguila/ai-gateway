package gauss

import (
	"bufio"
	"encoding/json"
	"io"
	"strings"

	gateway "github.com/raulaguila/ai-gateway"
)

// Thoth accepts complete data JSON on individual lines, including at EOF.
// Normalize those frames locally without weakening the generic SSE parser.
type eventBody struct {
	body      io.ReadCloser
	limited   *io.LimitedReader
	scanner   *bufio.Scanner
	pending   []byte
	multiline bool
}

func newEventBody(body io.ReadCloser, maxBytes int64, maxLine int) *eventBody {
	limited := &io.LimitedReader{R: body, N: maxBytes + 1}
	scanner := bufio.NewScanner(limited)
	scanner.Buffer(make([]byte, 0, min(4096, maxLine+2)), maxLine+2)
	return &eventBody{body: body, limited: limited, scanner: scanner}
}

func (b *eventBody) Read(destination []byte) (int, error) {
	if len(destination) == 0 {
		return 0, nil
	}
	if len(b.pending) == 0 {
		if !b.scanner.Scan() {
			if err := b.scanner.Err(); err != nil {
				return 0, err
			}
			return 0, io.EOF
		}
		if b.limited.N <= 0 {
			return 0, failure(gateway.InvalidResponse, "stream", "Gauss stream exceeds response limit", nil)
		}
		line := b.scanner.Text()
		b.pending = []byte(line + "\n")
		if line == "" {
			b.multiline = false
		} else if strings.HasPrefix(line, "data:") {
			payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if !b.multiline && (payload == "[DONE]" || json.Valid([]byte(payload))) {
				b.pending = append(b.pending, '\n')
			} else {
				b.multiline = true
			}
		}
	}
	n := copy(destination, b.pending)
	b.pending = b.pending[n:]
	return n, nil
}

func (b *eventBody) Close() error { return b.body.Close() }
