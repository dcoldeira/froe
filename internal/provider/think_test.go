package provider

import "testing"

// feedAll streams chunks through one filter, as a backend would deliver them.
func feedAll(chunks ...string) (text, reasoning string) {
	var f thinkFilter
	for _, c := range chunks {
		t, r := f.feed(c)
		text += t
		reasoning += r
	}
	return text + f.flush(), reasoning
}

func TestThinkFilterSeparatesAnInlineBlock(t *testing.T) {
	text, reasoning := feedAll("<think>the switch is causally nonseparable</think>W_sw is valid.")
	if text != "W_sw is valid." {
		t.Errorf("text = %q", text)
	}
	if reasoning != "the switch is causally nonseparable" {
		t.Errorf("reasoning = %q", reasoning)
	}
}

// Tags arrive split across chunks; a partial tag must be held, not printed.
func TestThinkFilterHandlesTagsSplitAcrossChunks(t *testing.T) {
	text, reasoning := feedAll("<thi", "nk>check r* = sqrt(2)-1</th", "ink>", "Robustness is 0.414.")
	if text != "Robustness is 0.414." {
		t.Errorf("text = %q", text)
	}
	if reasoning != "check r* = sqrt(2)-1" {
		t.Errorf("reasoning = %q", reasoning)
	}
}

// A thinking budget truncates the open tag away; a lone close tag still marks
// everything before it as reasoning.
func TestThinkFilterTreatsTextBeforeALoneCloseTagAsReasoning(t *testing.T) {
	text, reasoning := feedAll("so the witness is negative</think>", "Tr[Ω W] < 0.")
	if text != "Tr[Ω W] < 0." {
		t.Errorf("text = %q", text)
	}
	if reasoning != "so the witness is negative" {
		t.Errorf("reasoning = %q", reasoning)
	}
}

// Something that only looked like the start of a tag is ordinary text.
func TestThinkFilterFlushesAnUnfinishedLookalike(t *testing.T) {
	text, reasoning := feedAll("P_win < 0.8536 <th")
	if text != "P_win < 0.8536 <th" || reasoning != "" {
		t.Errorf("text = %q, reasoning = %q", text, reasoning)
	}
}

func TestThinkFilterPassesPlainTextThrough(t *testing.T) {
	text, reasoning := feedAll("CHSH ", "S = 2.61")
	if text != "CHSH S = 2.61" || reasoning != "" {
		t.Errorf("text = %q, reasoning = %q", text, reasoning)
	}
}

func TestDanglingPrefix(t *testing.T) {
	for _, tc := range []struct {
		s    string
		want int
	}{
		{"abc<", 1},
		{"abc<thi", 4},
		{"abc</thin", 6},
		{"abc", 0},
		{"<think>", 0}, // a complete tag is not dangling
	} {
		if got := danglingPrefix(tc.s, thinkOpen, thinkClose); got != tc.want {
			t.Errorf("danglingPrefix(%q) = %d, want %d", tc.s, got, tc.want)
		}
	}
}
