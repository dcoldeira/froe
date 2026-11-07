package agent

import (
	"strings"
	"testing"
)

// The approval for a three-line guard shows those three lines, not the whole
// function twice (seen 2026-11-07: every line of Average marked - and +).
func TestDiffShowsOnlyTheChange(t *testing.T) {
	old := `// Average returns the mean of nums.
func Average(nums []int) int {
	total := 0
	for _, n := range nums {
		total += n
	}
	return total / len(nums)
}`
	neu := `// Average returns the mean of nums.
func Average(nums []int) int {
	if len(nums) == 0 {
		return 0
	}
	total := 0
	for _, n := range nums {
		total += n
	}
	return total / len(nums)
}`
	got := diff(old, neu)
	want := `  // Average returns the mean of nums.
  func Average(nums []int) int {
+ 	if len(nums) == 0 {
+ 		return 0
+ 	}
  	total := 0
  	for _, n := range nums {`
	if got != want {
		t.Fatalf("diff:\n%s\nwant:\n%s", got, want)
	}
}

func TestDiffReplacedLineAndGap(t *testing.T) {
	old := "a\nb\nc\nd\ne\nf\ng\nh\ni"
	neu := "a\nB\nc\nd\ne\nf\ng\nH\ni"
	got := diff(old, neu)
	want := "  a\n- b\n+ B\n  c\n  d\n  …\n  f\n  g\n- h\n+ H\n  i"
	if got != want {
		t.Fatalf("diff:\n%s\nwant:\n%s", got, want)
	}
}

func TestPreviewSingularLine(t *testing.T) {
	if got := preview("1\n2\n3", 2); !strings.HasSuffix(got, "… 1 more line") {
		t.Errorf("preview = %q", got)
	}
	if got := preview("1\n2\n3\n4", 2); !strings.HasSuffix(got, "… 2 more lines") {
		t.Errorf("preview = %q", got)
	}
}
