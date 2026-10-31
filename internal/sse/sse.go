package sse

import (
	"bufio"
	"errors"
	gateway "github.com/raulaguila/ai-gateway"
	"io"
	"strings"
)

// Read dispatches complete events, not individual data lines. Unfinished
// events at EOF are discarded according to SSE framing rules.
func Read(r io.Reader, maxEvent int, consume func(string, string) error) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, min(4096, maxEvent+2)), maxEvent+2)
	var data strings.Builder
	var kind string
	first := true
	for scanner.Scan() {
		line := scanner.Text()
		if first {
			line = strings.TrimPrefix(line, "\ufeff")
			first = false
		}
		if len(line) > maxEvent {
			return &gateway.Error{Code: gateway.InvalidResponse, Operation: "stream", Message: "SSE line exceeds configured limit"}
		}
		if line == "" {
			if data.Len() > 0 {
				if err := consume(kind, strings.TrimSuffix(data.String(), "\n")); err != nil {
					return err
				}
			}
			data.Reset()
			kind = ""
			continue
		}
		field, value, _ := strings.Cut(line, ":")
		value = strings.TrimPrefix(value, " ")
		switch field {
		case "event":
			kind = value
		case "data":
			if data.Len()+len(value)+1 > maxEvent {
				return &gateway.Error{Code: gateway.InvalidResponse, Operation: "stream", Message: "SSE event exceeds configured limit"}
			}
			data.WriteString(value)
			data.WriteByte('\n')
		}
	}
	if errors.Is(scanner.Err(), bufio.ErrTooLong) {
		return &gateway.Error{Code: gateway.InvalidResponse, Operation: "stream", Message: "SSE line exceeds configured limit"}
	}
	return scanner.Err()
}
