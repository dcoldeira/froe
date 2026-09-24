package tools

import "testing"

// Lines from the 10-locate-real-shape fixture. The report says "Causal Order";
// the real sites spell it three ways, and the lookalike table says "Ordering".
func TestEndsAtWordKeepsSitesAndDropsTheLookalike(t *testing.T) {
	literal := Search{Pattern: "Causal Order", Literal: true, IgnoreCase: true}
	relaxed, ok := RelaxSeparators(literal)
	if !ok {
		t.Fatal("expected a relaxed variant")
	}
	for text, want := range map[string]bool{
		`            'Causal\nOrder', 'P_win',`:                             true,
		`                _s(data.get('causal_order', 'indefinite')),`:       true,
		`        'Causal Order', 'P_win',`:                                  true,
		`    "CausalOrder": "causal_order",`:                                true,
		`def add_causal_order_table(pdf):`:                                  true,
		`class CausalOrderTable:`:                                           true,
		`            'Causal\nOrdering',`:                                   false,
		`        'Node', 'Parents', 'Markov Condition', 'Causal Ordering',`: false,
	} {
		if got := EndsAtWord(text, relaxed); got != want {
			t.Errorf("relaxed EndsAtWord(%q) = %v, want %v", text, got, want)
		}
	}
	// The model's own literal search has the same flaw and the same fix.
	if EndsAtWord(`'Causal Ordering',`, literal) {
		t.Error("literal search kept the lookalike")
	}
	if !EndsAtWord(`'Causal Order', 'P_win',`, literal) {
		t.Error("literal search dropped the real site")
	}
}
