package provider

import "strings"

// thinkTags are the inline markers reasoning models emit when the backend does
// not split reasoning into its own field.
const (
	thinkOpen  = "<think>"
	thinkClose = "</think>"
)

// thinkFilter separates inline <think> blocks out of a streamed content field.
//
// Well-behaved backends put reasoning in `reasoning_content` and this never
// triggers. LM Studio serving Bonsai does not always: when a thinking budget
// truncates the reasoning, the remainder arrives inline in `content`, tags and
// all, and lands on the user's terminal. Measured 2026-09-11 — a one-line answer
// came back preceded by a paragraph of deliberation and a stray `</think>`.
//
// Tags can be split across streamed chunks ("<thi" then "nk>"), so a partial
// tag at the end of a chunk is held back rather than emitted.
type thinkFilter struct {
	inThink bool
	pending string
}

// feed consumes a content delta and returns the visible text and the reasoning
// text extracted from it.
func (f *thinkFilter) feed(s string) (text, reasoning string) {
	buf := f.pending + s
	f.pending = ""

	var out, think strings.Builder
	for len(buf) > 0 {
		if f.inThink {
			i := strings.Index(buf, thinkClose)
			if i < 0 {
				keep := danglingPrefix(buf, thinkClose)
				think.WriteString(buf[:len(buf)-keep])
				f.pending = buf[len(buf)-keep:]
				break
			}
			think.WriteString(buf[:i])
			buf = buf[i+len(thinkClose):]
			f.inThink = false
			continue
		}

		i := strings.Index(buf, thinkOpen)
		if i < 0 {
			// A close tag with no open: the backend truncated mid-reasoning, so
			// everything before it was thought, not answer.
			if j := strings.Index(buf, thinkClose); j >= 0 {
				think.WriteString(buf[:j])
				buf = buf[j+len(thinkClose):]
				continue
			}
			keep := danglingPrefix(buf, thinkOpen, thinkClose)
			out.WriteString(buf[:len(buf)-keep])
			f.pending = buf[len(buf)-keep:]
			break
		}
		out.WriteString(buf[:i])
		buf = buf[i+len(thinkOpen):]
		f.inThink = true
	}
	return out.String(), think.String()
}

// flush returns anything held back when the stream ends. A partial tag that
// never completed was ordinary text after all.
func (f *thinkFilter) flush() string {
	s := f.pending
	f.pending = ""
	return s
}

// danglingPrefix returns how many trailing bytes of s could still be the start
// of one of the given tags.
func danglingPrefix(s string, tags ...string) int {
	max := 0
	for _, tag := range tags {
		for n := len(tag) - 1; n > 0; n-- {
			if n <= len(s) && strings.HasPrefix(tag, s[len(s)-n:]) {
				if n > max {
					max = n
				}
				break
			}
		}
	}
	return max
}
