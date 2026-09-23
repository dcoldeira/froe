package evaltask

import "testing"

func TestAverage(t *testing.T) {
	if got := Average([]int{2, 4, 6}); got != 4 {
		t.Errorf("Average = %d, want 4", got)
	}
}

func TestAverageEmpty(t *testing.T) {
	// Must not panic.
	if got := Average(nil); got != 0 {
		t.Errorf("Average(nil) = %d, want 0", got)
	}
}
