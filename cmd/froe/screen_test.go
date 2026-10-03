package main

import (
	"testing"

	"github.com/dcoldeira/froe/internal/perms"
)

func TestVisibleWidthSkipsEscapesAndCountsWideRunes(t *testing.T) {
	if n := visibleWidth("\x1b[2mdim\x1b[0m text"); n != 8 {
		t.Errorf("styled text: %d, want 8", n)
	}
	if n := visibleWidth("日本"); n != 4 {
		t.Errorf("wide runes: %d, want 4", n)
	}
	if rows := visibleRows("", 10); rows != 0 {
		t.Errorf("empty line takes %d rows", rows)
	}
	if rows := visibleRows("0123456789", 10); rows != 1 {
		t.Errorf("an exactly full row is %d rows, want 1", rows)
	}
}

// Cutting a long streamed line must keep its escapes and not lose text.
func TestSplitVisibleKeepsEscapesWithTheHead(t *testing.T) {
	head, tail := splitVisible("\x1b[2mabcdef\x1b[0m", 4)
	if head != "\x1b[2mabcd" || tail != "ef\x1b[0m" {
		t.Errorf("got %q | %q", head, tail)
	}
	if visibleWidth(head) != 4 {
		t.Errorf("head is %d columns", visibleWidth(head))
	}
}

func TestApprovalKeys(t *testing.T) {
	for k, want := range map[key]perms.Decision{
		{kind: kRune, r: 'y'}: perms.Allow,
		{kind: kRune, r: 'a'}: perms.AlwaysAllow,
		{kind: kRune, r: 'n'}: perms.Deny,
		{kind: kEnter}:        perms.Deny,
		{kind: kInterrupt}:    perms.Deny,
	} {
		if d, ok := answerKey(k); !ok || d != want {
			t.Errorf("%+v: got %v %v, want %v", k, d, ok, want)
		}
	}
	if _, ok := answerKey(key{kind: kRune, r: 'x'}); ok {
		t.Error("an unrelated key answered the question")
	}
}
