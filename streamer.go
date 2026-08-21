package agentloop

import "strings"

// responseStreamer routes LLM streaming chunks to the run's event
// callback, but only once it's confident the current iteration is a
// final-response turn (not a JS code-emission turn).
//
// Detection looks at the accumulated prefix:
//
//   - leading "```"     → JS turn; emit nothing the whole iteration
//   - leading "DONE\n"  → final turn; strip the marker and emit the rest
//   - anything else     → free-form text fallback; emit as a final turn
//
// The first chunks are held back (32 chars or until a newline, whichever
// comes first) so a partial "DON" isn't ambiguous.
type responseStreamer struct {
	onEvent func(RunEvent)
	buf     strings.Builder
	mode    string // "" until decided, then "js" | "final"
}

// newResponseStreamer constructs a streamer that forwards final-text
// chunks to onEvent as RunEvents of type "response_chunk". onEvent may
// be nil — in that case the streamer is a no-op (still accumulates for
// the post-stream parser).
func newResponseStreamer(onEvent func(RunEvent)) *responseStreamer {
	return &responseStreamer{onEvent: onEvent}
}

func (s *responseStreamer) onChunk(chunk string) {
	s.buf.WriteString(chunk)
	if s.mode == "" {
		prefix := strings.TrimLeft(s.buf.String(), " \t\r\n")
		if strings.HasPrefix(prefix, "```") {
			s.mode = "js"
			return
		}
		if strings.HasPrefix(prefix, "DONE\n") || strings.HasPrefix(prefix, "DONE\r\n") {
			s.mode = "final"
			nl := strings.IndexByte(prefix, '\n')
			if nl >= 0 && len(prefix) > nl+1 {
				s.emitFinal(prefix[nl+1:])
			}
			return
		}
		if s.buf.Len() < 32 && !strings.ContainsRune(prefix, '\n') {
			return
		}
		s.mode = "final"
		s.emitFinal(prefix)
		return
	}
	if s.mode == "final" {
		s.emitFinal(chunk)
	}
}

func (s *responseStreamer) emitFinal(text string) {
	if text == "" || s.onEvent == nil {
		return
	}
	s.onEvent(RunEvent{Type: "response_chunk", Content: text})
}
