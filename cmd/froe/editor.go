package main

import (
	"errors"
	"io"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// The chat prompt's editing and key decoding: a multi-line editor, drawn by
// screen.go as a box between two rules with a hint line under it.
//
// It replaces x/term's Terminal, which edits a single line and lets a long
// prompt wrap wherever the terminal breaks it - mid-word, with nothing
// separating it from the model output above. Here Enter sends, and a modified
// Enter (Shift, Ctrl+Shift, Alt) or Ctrl+J inserts a newline, as does a
// backslash typed just before Enter.
//
// Modified Enter only reaches the program if the terminal reports it. Two
// reporting modes are requested, and both encodings
// are decoded: xterm's modifyOtherKeys (what tmux speaks with
// `extended-keys on`; with `extended-keys-format csi-u` it sends CSI 13;2u)
// and the kitty keyboard protocol (Ghostty, kitty, WezTerm without tmux).
// screen.go turns them on for the whole chat and off again when it ends.

const (
	modesOn  = "\x1b[?2004h\x1b[>4;1m\x1b[>1u" // bracketed paste, modifyOtherKeys 1, kitty disambiguate
	modesOff = "\x1b[?2004l\x1b[>4m\x1b[<u"
)

var errExit = errors.New("exit")

// editor holds the text being typed. It knows nothing about the terminal, so
// its behaviour is tested without one.
type editor struct {
	buf     []rune
	pos     int
	history []string
	hidx    int    // == len(history) while editing a fresh prompt
	draft   []rune // the fresh prompt, kept while browsing history
	armed   bool   // Ctrl+C on an empty prompt once; a second exits
}

type keyKind int

const (
	kNone keyKind = iota
	kRune
	kText // a paste, inserted verbatim
	kEnter
	kNewline
	kBackspace
	kDelete
	kLeft
	kRight
	kUp
	kDown
	kHome
	kEnd
	kWordLeft
	kWordRight
	kDeleteWord
	kKillStart
	kKillEnd
	kInterrupt
	kEOF
	kRedraw
)

type key struct {
	kind keyKind
	r    rune
	text string
}

// apply edits the buffer for one key. It returns submit when the prompt
// should be sent, and errExit or io.EOF when the user is leaving.
func (e *editor) apply(k key) (submit bool, err error) {
	if k.kind != kInterrupt {
		e.armed = false
	}
	switch k.kind {
	case kRune:
		e.insert([]rune{k.r})
	case kText:
		e.insert([]rune(k.text))
	case kNewline:
		e.insert([]rune{'\n'})
	case kEnter:
		// A trailing backslash continues the line, for terminals that cannot
		// tell a modified Enter from a plain one.
		if e.pos > 0 && e.buf[e.pos-1] == '\\' && (e.pos == len(e.buf) || e.buf[e.pos] == '\n') {
			e.buf[e.pos-1] = '\n'
			return false, nil
		}
		return strings.TrimSpace(string(e.buf)) != "", nil
	case kBackspace:
		if e.pos > 0 {
			e.buf = append(e.buf[:e.pos-1], e.buf[e.pos:]...)
			e.pos--
		}
	case kDelete:
		if e.pos < len(e.buf) {
			e.buf = append(e.buf[:e.pos], e.buf[e.pos+1:]...)
		}
	case kLeft:
		if e.pos > 0 {
			e.pos--
		}
	case kRight:
		if e.pos < len(e.buf) {
			e.pos++
		}
	case kHome:
		e.pos = e.lineStart()
	case kEnd:
		e.pos = e.lineEnd()
	case kWordLeft:
		e.pos = e.wordLeft()
	case kWordRight:
		for e.pos < len(e.buf) && !isWord(e.buf[e.pos]) {
			e.pos++
		}
		for e.pos < len(e.buf) && isWord(e.buf[e.pos]) {
			e.pos++
		}
	case kDeleteWord:
		to := e.wordLeft()
		e.buf = append(e.buf[:to], e.buf[e.pos:]...)
		e.pos = to
	case kKillStart:
		start := e.lineStart()
		e.buf = append(e.buf[:start], e.buf[e.pos:]...)
		e.pos = start
	case kKillEnd:
		e.buf = append(e.buf[:e.pos], e.buf[e.lineEnd():]...)
	case kInterrupt:
		if len(e.buf) > 0 {
			e.buf, e.pos = nil, 0
			return false, nil
		}
		if e.armed {
			return false, errExit
		}
		e.armed = true
	case kEOF:
		if len(e.buf) == 0 {
			return false, io.EOF
		}
		if e.pos < len(e.buf) {
			e.buf = append(e.buf[:e.pos], e.buf[e.pos+1:]...)
		}
	}
	return false, nil
}

func (e *editor) insert(rs []rune) {
	clean := make([]rune, 0, len(rs))
	for _, r := range rs {
		switch {
		case r == '\t':
			// A tab has no fixed width on screen, which would put the cursor
			// in the wrong place; the prompt is prose, so spaces lose nothing.
			clean = append(clean, ' ', ' ', ' ', ' ')
		case r == '\n' || !unicode.IsControl(r):
			clean = append(clean, r)
		}
	}
	tail := append(clean, e.buf[e.pos:]...)
	e.buf = append(e.buf[:e.pos], tail...)
	e.pos += len(clean)
}

func (e *editor) lineStart() int {
	i := e.pos
	for i > 0 && e.buf[i-1] != '\n' {
		i--
	}
	return i
}

func (e *editor) lineEnd() int {
	i := e.pos
	for i < len(e.buf) && e.buf[i] != '\n' {
		i++
	}
	return i
}

func (e *editor) wordLeft() int {
	i := e.pos
	for i > 0 && !isWord(e.buf[i-1]) {
		i--
	}
	for i > 0 && isWord(e.buf[i-1]) {
		i--
	}
	return i
}

func isWord(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' }

// vertical moves the cursor one screen row up (dir -1) or down (+1) at the
// given width, falling through to history at the top or bottom row.
func (e *editor) vertical(dir, width int) {
	rows, at := layout(e.buf, width)
	cur := at[e.pos]
	target := cur.row + dir
	if target < 0 || target >= len(rows) {
		e.browse(dir)
		return
	}
	sp := rows[target]
	i := sp.start + cur.col
	if i > sp.end {
		i = sp.end
	}
	// The end of a wrapped row is drawn at the start of the next one.
	if i > sp.start && at[i].row != target {
		i--
	}
	e.pos = i
}

func (e *editor) browse(dir int) {
	next := e.hidx + dir
	if next < 0 || next > len(e.history) {
		return
	}
	if e.hidx == len(e.history) {
		e.draft = append([]rune(nil), e.buf...)
	}
	e.hidx = next
	if next == len(e.history) {
		e.buf = append([]rune(nil), e.draft...)
	} else {
		e.buf = []rune(e.history[next])
	}
	e.pos = len(e.buf)
}

// take empties the editor, recording the text in history.
func (e *editor) take() string {
	s := string(e.buf)
	if t := strings.TrimSpace(s); t != "" && (len(e.history) == 0 || e.history[len(e.history)-1] != s) {
		e.history = append(e.history, s)
	}
	e.buf, e.pos, e.draft = nil, 0, nil
	e.hidx = len(e.history)
	return s
}

// span is one screen row of the buffer: buf[start:end], without its '\n'.
type span struct{ start, end int }

type cell struct{ row, col int }

// layout wraps buf into rows of at most width runes, breaking at newlines and
// preferring to break after a space. It returns the rows and, for every
// cursor position 0..len(buf), where that position is drawn.
//
// A cursor after a full row sits at col == width, so the caller must leave a
// spare column for it.
func layout(buf []rune, width int) ([]span, []cell) {
	if width < 1 {
		width = 1
	}
	at := make([]cell, len(buf)+1)
	var rows []span
	start, row := 0, 0
	for i := 0; i < len(buf); i++ {
		if buf[i] == '\n' {
			at[i] = cell{row, i - start}
			rows = append(rows, span{start, i})
			row++
			start = i + 1
			continue
		}
		if i-start == width {
			brk := i
			for j := i - 1; j > start; j-- {
				if buf[j] == ' ' {
					brk = j + 1
					break
				}
			}
			rows = append(rows, span{start, brk})
			row++
			start = brk
			for j := brk; j < i; j++ {
				at[j] = cell{row, j - start}
			}
		}
		at[i] = cell{row, i - start}
	}
	at[len(buf)] = cell{row, len(buf) - start}
	rows = append(rows, span{start, len(buf)})
	return rows, at
}

// decode turns raw terminal input into keys. Bytes that may be the start of
// an unfinished sequence are returned as rest, to be retried with more input.
func decode(b []byte) (keys []key, rest []byte) {
	for len(b) > 0 {
		k, n := decodeOne(b)
		if n == 0 {
			return keys, b
		}
		if k.kind != kNone {
			keys = append(keys, k)
		}
		b = b[n:]
	}
	return keys, nil
}

func decodeOne(b []byte) (key, int) {
	c := b[0]
	if c == 0x1b {
		return decodeEscape(b)
	}
	if c < 0x20 || c == 0x7f {
		return controlKey(c), 1
	}
	if !utf8.FullRune(b) {
		return key{}, 0
	}
	r, n := utf8.DecodeRune(b)
	return key{kind: kRune, r: r}, n
}

func controlKey(c byte) key {
	switch c {
	case '\r':
		return key{kind: kEnter}
	case '\n':
		return key{kind: kNewline}
	case '\t':
		return key{kind: kRune, r: '\t'}
	case 0x7f, 0x08:
		return key{kind: kBackspace}
	case 0x01:
		return key{kind: kHome}
	case 0x02:
		return key{kind: kLeft}
	case 0x03:
		return key{kind: kInterrupt}
	case 0x04:
		return key{kind: kEOF}
	case 0x05:
		return key{kind: kEnd}
	case 0x06:
		return key{kind: kRight}
	case 0x0b:
		return key{kind: kKillEnd}
	case 0x0c:
		return key{kind: kRedraw}
	case 0x0e:
		return key{kind: kDown}
	case 0x10:
		return key{kind: kUp}
	case 0x15:
		return key{kind: kKillStart}
	case 0x17:
		return key{kind: kDeleteWord}
	}
	return key{}
}

const (
	pasteStart = "\x1b[200~"
	pasteEnd   = "\x1b[201~"
)

func decodeEscape(b []byte) (key, int) {
	if len(b) == 1 {
		// A lone Escape. Sequences arrive in one read, so this is the key.
		return key{}, 1
	}
	switch b[1] {
	case '[':
		if strings.HasPrefix(string(b), pasteStart) {
			end := strings.Index(string(b), pasteEnd)
			if end < 0 {
				return key{}, 0
			}
			text := string(b[len(pasteStart):end])
			text = strings.ReplaceAll(text, "\r\n", "\n")
			text = strings.ReplaceAll(text, "\r", "\n")
			return key{kind: kText, text: text}, end + len(pasteEnd)
		}
		return decodeCSI(b)
	case 'O':
		if len(b) < 3 {
			return key{}, 0
		}
		return arrowKey(b[2], 1), 3
	case '\r', '\n':
		return key{kind: kNewline}, 2 // Alt+Enter
	case 'b':
		return key{kind: kWordLeft}, 2
	case 'f':
		return key{kind: kWordRight}, 2
	case 0x7f, 0x08:
		return key{kind: kDeleteWord}, 2
	}
	return key{}, 2
}

// decodeCSI reads ESC [ params final.
func decodeCSI(b []byte) (key, int) {
	i := 2
	for i < len(b) && (b[i] < 0x40 || b[i] > 0x7e) {
		i++
	}
	if i == len(b) {
		return key{}, 0
	}
	final := b[i]
	params := strings.Split(string(b[2:i]), ";")
	num := func(n int) int {
		if n >= len(params) {
			return 0
		}
		// Kitty may add ":alternate" keys; only the first value matters.
		v, _ := strconv.Atoi(strings.SplitN(params[n], ":", 2)[0])
		return v
	}
	n := i + 1

	switch final {
	case 'A', 'B', 'C', 'D', 'H', 'F':
		return arrowKey(final, num(1)), n
	case 'u': // kitty / tmux csi-u: code;mods
		return modifiedKey(num(0), num(1)), n
	case '~':
		if num(0) == 27 { // xterm modifyOtherKeys: 27;mods;code
			return modifiedKey(num(2), num(1)), n
		}
		switch num(0) {
		case 1, 7:
			return key{kind: kHome}, n
		case 4, 8:
			return key{kind: kEnd}, n
		case 3:
			return key{kind: kDelete}, n
		}
	}
	return key{}, n
}

func arrowKey(final byte, mods int) key {
	word := mods > 1 && (mods-1)&(2|4) != 0 // Alt or Ctrl
	switch final {
	case 'A':
		return key{kind: kUp}
	case 'B':
		return key{kind: kDown}
	case 'C':
		if word {
			return key{kind: kWordRight}
		}
		return key{kind: kRight}
	case 'D':
		if word {
			return key{kind: kWordLeft}
		}
		return key{kind: kLeft}
	case 'H':
		return key{kind: kHome}
	case 'F':
		return key{kind: kEnd}
	}
	return key{}
}

// modifiedKey maps a key code plus xterm modifier value (1 + shift 1, alt 2,
// ctrl 4) to a key.
func modifiedKey(code, mods int) key {
	m := 0
	if mods > 1 {
		m = mods - 1
	}
	shift, alt, ctrl := m&1 != 0, m&2 != 0, m&4 != 0
	switch code {
	case 13:
		if m != 0 {
			return key{kind: kNewline}
		}
		return key{kind: kEnter}
	case 27:
		return key{}
	case 9:
		return key{kind: kRune, r: '\t'}
	case 127, 8:
		if alt || ctrl {
			return key{kind: kDeleteWord}
		}
		return key{kind: kBackspace}
	}
	if ctrl && code < 128 && unicode.IsLetter(rune(code)) {
		return controlKey(byte(unicode.ToLower(rune(code))) & 0x1f)
	}
	if alt {
		switch code {
		case 'b':
			return key{kind: kWordLeft}
		case 'f':
			return key{kind: kWordRight}
		}
		return key{}
	}
	if code >= 0x20 && !ctrl {
		r := rune(code)
		if shift {
			r = unicode.ToUpper(r)
		}
		return key{kind: kRune, r: r}
	}
	return key{}
}
