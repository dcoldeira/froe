package main

import (
	"io"
	"testing"
)

func kinds(ks []key) []keyKind {
	out := make([]keyKind, len(ks))
	for i, k := range ks {
		out[i] = k.kind
	}
	return out
}

// Every way a terminal can report "newline, not send".
func TestDecodeModifiedEnterIsNewline(t *testing.T) {
	for name, in := range map[string]string{
		"tmux csi-u shift+enter":      "\x1b[13;2u",
		"tmux csi-u ctrl+shift+enter": "\x1b[13;6u",
		"kitty ctrl+enter":            "\x1b[13;5u",
		"xterm shift+enter":           "\x1b[27;2;13~",
		"xterm ctrl+shift+enter":      "\x1b[27;6;13~",
		"alt+enter":                   "\x1b\r",
		"ctrl+j":                      "\n",
	} {
		ks, rest := decode([]byte(in))
		if len(ks) != 1 || ks[0].kind != kNewline || rest != nil {
			t.Errorf("%s: got %v rest %q, want one newline", name, kinds(ks), rest)
		}
	}
	if ks, _ := decode([]byte("\r")); len(ks) != 1 || ks[0].kind != kEnter {
		t.Errorf("plain enter: got %v", kinds(ks))
	}
	if ks, _ := decode([]byte("\x1b[13u")); len(ks) != 1 || ks[0].kind != kEnter {
		t.Errorf("kitty plain enter: got %v", kinds(ks))
	}
}

// With the kitty protocol on, Ctrl+C and friends arrive as CSI u.
func TestDecodeKittyControlKeys(t *testing.T) {
	ks, _ := decode([]byte("\x1b[99;5u\x1b[100;5u\x1b[97;6u"))
	want := []keyKind{kInterrupt, kEOF, kHome}
	if got := kinds(ks); len(got) != 3 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestDecodeKeepsUnfinishedInput(t *testing.T) {
	ks, rest := decode([]byte("ab\x1b[13;"))
	if len(ks) != 2 || string(rest) != "\x1b[13;" {
		t.Fatalf("got %v rest %q", kinds(ks), rest)
	}
	_, rest = decode([]byte("\x1b[200~line one\r\nline"))
	if rest == nil {
		t.Fatal("an unfinished paste must wait for the rest")
	}
	ks, _ = decode([]byte("é"[:1]))
	if len(ks) != 0 {
		t.Fatal("half a UTF-8 rune must wait for the rest")
	}
}

func TestPasteIsOneBlockWithNewlines(t *testing.T) {
	ks, rest := decode([]byte("\x1b[200~one\r\ntwo\rthree\x1b[201~x"))
	if rest != nil || len(ks) != 2 || ks[0].kind != kText || ks[0].text != "one\ntwo\nthree" || ks[1].r != 'x' {
		t.Fatalf("got %+v rest %q", ks, rest)
	}
	var e editor
	submit, _ := e.apply(ks[0])
	if submit || string(e.buf) != "one\ntwo\nthree" {
		t.Fatalf("a pasted newline must not send: submit=%v buf=%q", submit, string(e.buf))
	}
}

func typeText(e *editor, s string) {
	for _, r := range s {
		e.apply(key{kind: kRune, r: r})
	}
}

func TestEnterSendsNewlineDoesNot(t *testing.T) {
	var e editor
	typeText(&e, "first")
	if submit, _ := e.apply(key{kind: kNewline}); submit {
		t.Fatal("newline sent the prompt")
	}
	typeText(&e, "second")
	if submit, _ := e.apply(key{kind: kEnter}); !submit {
		t.Fatal("enter did not send")
	}
	if got := e.take(); got != "first\nsecond" {
		t.Fatalf("got %q", got)
	}
	if submit, _ := e.apply(key{kind: kEnter}); submit {
		t.Fatal("an empty prompt was sent")
	}
}

func TestTrailingBackslashContinues(t *testing.T) {
	var e editor
	typeText(&e, `one\`)
	if submit, _ := e.apply(key{kind: kEnter}); submit {
		t.Fatal("backslash-enter sent the prompt")
	}
	typeText(&e, "two")
	if string(e.buf) != "one\ntwo" {
		t.Fatalf("got %q", string(e.buf))
	}
}

func TestCtrlCClearsThenExits(t *testing.T) {
	var e editor
	typeText(&e, "draft")
	if _, err := e.apply(key{kind: kInterrupt}); err != nil || len(e.buf) != 0 {
		t.Fatalf("first ctrl+c should clear: err=%v buf=%q", err, string(e.buf))
	}
	if _, err := e.apply(key{kind: kInterrupt}); err != nil || !e.armed {
		t.Fatal("ctrl+c on an empty prompt should arm, not exit")
	}
	if _, err := e.apply(key{kind: kInterrupt}); err != errExit {
		t.Fatalf("second ctrl+c should exit, got %v", err)
	}
	if _, err := e.apply(key{kind: kEOF}); err != io.EOF {
		t.Fatalf("ctrl+d on empty should be EOF, got %v", err)
	}
}

func TestLayoutWrapsAtSpaces(t *testing.T) {
	rows, at := layout([]rune("this is test this is test"), 10)
	var got []string
	buf := []rune("this is test this is test")
	for _, r := range rows {
		got = append(got, string(buf[r.start:r.end]))
	}
	want := []string{"this is ", "test this ", "is test"}
	if len(got) != len(want) {
		t.Fatalf("got %q", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("row %d: got %q want %q (all %q)", i, got[i], want[i], got)
		}
	}
	if c := at[len(buf)]; c.row != 2 || c.col != 7 {
		t.Errorf("end cursor at %+v", c)
	}
	// A word longer than the row is cut, since there is nowhere to break.
	rows, _ = layout([]rune("abcdefghijkl"), 5)
	if len(rows) != 3 {
		t.Errorf("long word: %d rows", len(rows))
	}
}

func TestUpDownMoveRowsThenHistory(t *testing.T) {
	var e editor
	typeText(&e, "old")
	e.apply(key{kind: kEnter})
	e.take()

	typeText(&e, "abc")
	e.apply(key{kind: kNewline})
	typeText(&e, "defgh")
	e.vertical(-1, 40)
	if e.pos != 3 {
		t.Fatalf("up from row 2 col 5 should land at end of row 1, pos %d", e.pos)
	}
	e.vertical(-1, 40)
	if string(e.buf) != "old" {
		t.Fatalf("up on the top row should recall history, got %q", string(e.buf))
	}
	e.vertical(1, 40)
	if string(e.buf) != "abc\ndefgh" {
		t.Fatalf("down past history should restore the draft, got %q", string(e.buf))
	}
}
