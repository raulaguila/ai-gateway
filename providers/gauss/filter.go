package gauss

import (
	"strings"

	gateway "github.com/raulaguila/ai-gateway"
)

var reasoningTags = []string{"think", "thinking", "analysis", "reasoning"}

type visibleFilter struct {
	pending string
	stack   []string
}

// Only possible tag prefixes are buffered, so split tags never reach callbacks.
func (f *visibleFilter) write(text string) (string, error) {
	var output strings.Builder
	for i := 0; i < len(text); i++ {
		if f.pending == "" && text[i] != '<' {
			if len(f.stack) == 0 {
				output.WriteByte(text[i])
			}
			continue
		}
		f.pending += text[i : i+1]
		for f.pending != "" {
			candidate := strings.ToLower(f.pending)
			prefix, matched := false, false
			for _, name := range reasoningTags {
				for _, closing := range []bool{false, true} {
					tag := "<" + name + ">"
					if closing {
						tag = "</" + name + ">"
					}
					if candidate == tag {
						if !closing {
							if len(f.stack) >= 64 {
								return output.String(), failure(gateway.InvalidResponse, "", "reasoning blocks exceed nesting limit", nil)
							}
							f.stack = append(f.stack, name)
						} else if len(f.stack) > 0 && f.stack[len(f.stack)-1] == name {
							f.stack = f.stack[:len(f.stack)-1]
						}
						f.pending, matched = "", true
						break
					}
					prefix = prefix || strings.HasPrefix(tag, candidate)
				}
				if matched {
					break
				}
			}
			if matched || prefix {
				break
			}
			if len(f.stack) == 0 {
				output.WriteByte(f.pending[0])
			}
			f.pending = f.pending[1:]
		}
	}
	return output.String(), nil
}

// Partial tag prefixes and unterminated hidden blocks are deliberately discarded.
func (f *visibleFilter) finish() { f.pending = ""; f.stack = nil }
