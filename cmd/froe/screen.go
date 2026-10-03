package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/dcoldeira/froe/internal/perms"
	"golang.org/x/term"
)

// screen keeps the prompt box pinned at the bottom of the terminal for the
// whole chat, with everything else printed above it - the layout Claude Code
// uses. `froe chat` only; `froe rpc` (Neovim) and `froe do` never touch it.
//
// Output is never drawn into a scroll region. Inside tmux, lines that scroll
// out of a region that stops short of the bottom row are not kept in
// history, so a long answer could not be scrolled back to. Instead the box
// and the unfinished output line above it form a "live" area at the bottom:
// each update erases it, prints whatever output is now complete as ordinary
// lines (which scroll into history as normal), and draws the live area again.
//
// To catch every write without threading a writer through the renderer,
// os.Stdout and os.Stderr point at a pipe for the duration, and the screen
// reads the other end. The terminal is in raw mode throughout, so a single
// goroutine owns the keyboard: the box takes typing while the model works
// (Enter queues the prompt), Ctrl+C stops a run, and approvals are answered
// with a key instead of a line read behind the box's back.
type screen struct {
	mu  sync.Mutex
	ed  editor
	fd  int
	tty *os.File
	st  style

	saved            *term.State
	origOut, origErr *os.File
	pw               *os.File
	drained          chan struct{}
	stopTick         chan struct{}

	partial string // output after the last newline, drawn in the live area
	cursor  int    // rows from the top of the live area to the cursor

	running bool
	started time.Time
	cancel  context.CancelFunc
	asking  chan perms.Decision
	spin    int

	queue  []string
	wake   chan struct{}
	closed bool
	why    error
}

var spinFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

func newScreen(st style) (*screen, error) {
	s := &screen{
		fd: int(os.Stdin.Fd()), tty: os.Stderr, st: st,
		drained: make(chan struct{}), stopTick: make(chan struct{}),
		wake: make(chan struct{}, 1),
	}
	saved, err := term.MakeRaw(s.fd)
	if err != nil {
		return nil, err
	}
	pr, pw, err := os.Pipe()
	if err != nil {
		term.Restore(s.fd, saved)
		return nil, err
	}
	s.saved, s.pw = saved, pw
	s.origOut, s.origErr = os.Stdout, os.Stderr
	os.Stdout, os.Stderr = pw, pw
	fmt.Fprint(s.tty, modesOn)

	go s.readOutput(pr)
	go s.readKeys()
	go s.tick()

	s.mu.Lock()
	s.redraw("")
	s.mu.Unlock()
	return s, nil
}

// close hands the terminal back as it was found, after the last output.
func (s *screen) close() {
	os.Stdout, os.Stderr = s.origOut, s.origErr
	s.pw.Close()
	<-s.drained
	close(s.stopTick)

	s.mu.Lock()
	defer s.mu.Unlock()
	var b strings.Builder
	s.home(&b)
	if s.partial != "" {
		b.WriteString(s.partial + "\r\n")
		s.partial = ""
	}
	b.WriteString(modesOff + "\x1b[?25h")
	fmt.Fprint(s.tty, b.String())
	term.Restore(s.fd, s.saved)
}

// next blocks until a prompt has been submitted, and echoes it above the box.
func (s *screen) next() (string, error) {
	for {
		s.mu.Lock()
		if len(s.queue) > 0 {
			text := s.queue[0]
			s.queue = s.queue[1:]
			s.redraw("")
			s.mu.Unlock()
			// Through the pipe, so it lands after any output still in it.
			fmt.Fprint(os.Stderr, s.echo(text))
			return text, nil
		}
		if s.closed {
			s.mu.Unlock()
			return "", s.why
		}
		s.mu.Unlock()
		<-s.wake
	}
}

func (s *screen) signal() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// startRun shows the run as in progress; cancel stops it on Ctrl+C.
func (s *screen) startRun(cancel context.CancelFunc) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.running, s.started, s.cancel = true, time.Now(), cancel
	s.redraw("")
}

func (s *screen) endRun() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.running, s.cancel = false, nil
	s.redraw("")
}

// ask answers an approval request from a key typed into the screen. It is
// perms.Gate's Prompt, so the gate's policy still decides whether to ask.
func (s *screen) ask(req perms.Request) perms.Decision {
	var b strings.Builder
	fmt.Fprintf(&b, "\n  %s\n", req.Summary)
	if req.Detail != "" {
		for _, line := range strings.Split(strings.TrimRight(req.Detail, "\n"), "\n") {
			fmt.Fprintf(&b, "  │ %s\n", line)
		}
	}
	fmt.Fprint(os.Stderr, b.String())

	ch := make(chan perms.Decision, 1)
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return perms.Deny
	}
	s.asking = ch
	s.redraw("")
	s.mu.Unlock()

	d := <-ch
	label := map[perms.Decision]string{perms.Allow: "yes", perms.AlwaysAllow: "always", perms.Deny: "no"}[d]
	fmt.Fprintf(os.Stderr, "  %s\n", s.st.dim("allow? → "+label))
	return d
}

func (s *screen) readOutput(pr *os.File) {
	defer close(s.drained)
	buf := make([]byte, 8192)
	for {
		n, err := pr.Read(buf)
		if n > 0 {
			s.mu.Lock()
			s.redraw(string(buf[:n]))
			s.mu.Unlock()
		}
		if err != nil {
			return
		}
	}
}

func (s *screen) readKeys() {
	buf := make([]byte, 4096)
	var pending []byte
	for {
		n, err := os.Stdin.Read(buf)
		if err != nil {
			s.mu.Lock()
			s.leave(io.EOF)
			s.mu.Unlock()
			return
		}
		pending = append(pending, buf[:n]...)
		var keys []key
		keys, pending = decode(pending)
		s.mu.Lock()
		for _, k := range keys {
			s.handle(k)
		}
		s.redraw("")
		s.mu.Unlock()
	}
}

func (s *screen) tick() {
	t := time.NewTicker(100 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-s.stopTick:
			return
		case <-t.C:
			s.mu.Lock()
			if s.running {
				s.spin++
				s.redraw("")
			}
			s.mu.Unlock()
		}
	}
}

// leave records that the user is going, stopping any run and question.
func (s *screen) leave(why error) {
	if s.closed {
		return
	}
	s.closed, s.why = true, why
	if s.asking != nil {
		s.asking <- perms.Deny
		s.asking = nil
	}
	if s.cancel != nil {
		s.cancel()
	}
	s.signal()
}

// handle applies one key. Called with mu held.
func (s *screen) handle(k key) {
	if s.asking != nil {
		if d, ok := answerKey(k); ok {
			s.asking <- d
			s.asking = nil
		}
		return
	}
	switch k.kind {
	case kUp:
		s.ed.vertical(-1, s.textWidth())
		return
	case kDown:
		s.ed.vertical(1, s.textWidth())
		return
	case kInterrupt:
		if s.running && len(s.ed.buf) == 0 {
			s.cancel()
			return
		}
	case kEOF:
		if s.running && len(s.ed.buf) == 0 {
			return // leaving mid-run would drop the run's output
		}
	}
	submit, err := s.ed.apply(k)
	if err != nil {
		s.leave(err)
		return
	}
	if submit {
		s.queue = append(s.queue, s.ed.take())
		s.signal()
	}
}

func answerKey(k key) (perms.Decision, bool) {
	switch k.kind {
	case kRune:
		switch k.r {
		case 'y', 'Y':
			return perms.Allow, true
		case 'a', 'A':
			return perms.AlwaysAllow, true
		case 'n', 'N':
			return perms.Deny, true
		}
	case kEnter, kInterrupt:
		return perms.Deny, true
	}
	return perms.Deny, false
}

func (s *screen) width() int {
	w, _, err := term.GetSize(s.fd)
	if err != nil || w < 20 {
		return 80
	}
	return w
}

func (s *screen) height() int {
	_, h, err := term.GetSize(s.fd)
	if err != nil || h < 8 {
		return 24
	}
	return h
}

func (s *screen) textWidth() int { return s.width() - len([]rune(promptGlyph)) - 1 }

const promptGlyph = "› "

// echo formats a submitted prompt the way it was typed.
func (s *screen) echo(text string) string {
	runes := []rune(text)
	rows, _ := layout(runes, s.textWidth())
	var b strings.Builder
	for i, sp := range rows {
		lead := "  "
		if i == 0 {
			lead = s.st.dim(promptGlyph)
		}
		b.WriteString(lead + string(runes[sp.start:sp.end]) + "\n")
	}
	return b.String()
}

// home moves to the top of the live area and erases it.
func (s *screen) home(b *strings.Builder) {
	b.WriteString("\x1b[?25l")
	if s.cursor > 0 {
		fmt.Fprintf(b, "\x1b[%dA", s.cursor)
	}
	b.WriteString("\r\x1b[J")
	s.cursor = 0
}

// redraw prints out (new output, possibly empty) above the box and draws the
// live area again. Called with mu held.
func (s *screen) redraw(out string) {
	w := s.width()
	var b strings.Builder
	s.home(&b)

	text := s.partial + strings.ReplaceAll(out, "\t", "    ")
	if i := strings.LastIndexByte(text, '\n'); i >= 0 {
		b.WriteString(strings.ReplaceAll(text[:i+1], "\n", "\r\n"))
		text = text[i+1:]
	}
	// An unfinished line keeps growing as the model streams a paragraph.
	// Print all but its last screen row for good, so the live area cannot
	// outgrow the screen; the rows look the same as if the line had ended.
	if rows := visibleRows(text, w); rows > 2 {
		head, tail := splitVisible(text, (rows-1)*w)
		b.WriteString(head + "\r\n")
		text = tail
	}
	s.partial = text

	var lines []string // each fits in one row, except the partial line
	partialRows := 0
	if s.partial != "" {
		lines = append(lines, s.partial)
		partialRows = visibleRows(s.partial, w)
	}
	switch {
	case s.asking != nil:
		lines = append(lines, s.st.yellow("  allow? [y]es / [n]o / [a]lways this tool"))
	case s.running:
		secs := int(time.Since(s.started).Seconds())
		lines = append(lines, s.st.yellow(spinFrames[s.spin%len(spinFrames)])+
			s.st.dim(fmt.Sprintf(" working · %ds · ctrl+c to stop", secs)))
	}
	for _, q := range s.queue {
		first, _, _ := strings.Cut(q, "\n")
		lines = append(lines, s.st.dim(fitWidth("  ↳ queued: "+first, w-1)))
	}
	above := len(lines)
	if partialRows > 1 {
		above += partialRows - 1
	}

	box, cur := s.box(w, s.height()-above)
	lines = append(lines, box...)

	for i, l := range lines {
		if i > 0 {
			b.WriteString("\r\n")
		}
		b.WriteString(l)
	}
	total := above + len(box)
	row := above + cur.row
	if up := total - 1 - row; up > 0 {
		fmt.Fprintf(&b, "\x1b[%dA", up)
	}
	b.WriteString("\r")
	if cur.col > 0 {
		fmt.Fprintf(&b, "\x1b[%dC", cur.col)
	}
	if s.asking == nil {
		b.WriteString("\x1b[?25h")
	}
	s.cursor = row
	fmt.Fprint(s.tty, b.String())
}

// box draws rule, text rows, rule, hint in at most room rows, and says where
// the cursor goes within it.
func (s *screen) box(w, room int) ([]string, cell) {
	rows, at := layout(s.ed.buf, s.textWidth())
	cur := at[s.ed.pos]

	first, last := 0, len(rows)
	max := room - 3
	if max < 1 {
		max = 1
	}
	if len(rows) > max {
		first = cur.row - max + 1
		if first < 0 {
			first = 0
		}
		last = first + max
	}

	rule := s.st.dim(strings.Repeat("─", w))
	lines := []string{rule}
	for i := first; i < last; i++ {
		lead := "  "
		if i == 0 {
			lead = s.st.bold(promptGlyph)
		}
		lines = append(lines, lead+string(s.ed.buf[rows[i].start:rows[i].end]))
	}
	lines = append(lines, rule)

	var hint string
	switch {
	case s.ed.armed:
		hint = s.st.yellow("  press ctrl+c again to exit")
	case s.running:
		hint = s.st.dim("  enter queues the next prompt · shift+enter newline")
	default:
		hint = s.st.dim("  enter send · shift+enter newline · ctrl+d exit")
	}
	lines = append(lines, hint)
	return lines, cell{1 + cur.row - first, 2 + cur.col}
}

// visibleRows is how many screen rows s takes at width w; 0 for "".
func visibleRows(s string, w int) int {
	n := visibleWidth(s)
	if n == 0 {
		return 0
	}
	return (n + w - 1) / w
}

func visibleWidth(s string) int {
	n := 0
	for i := 0; i < len(s); {
		if l := escapeLen(s[i:]); l > 0 {
			i += l
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		n += runeWidth(r)
		i += size
	}
	return n
}

// splitVisible cuts s after cols visible columns, keeping escapes with the
// head.
func splitVisible(s string, cols int) (string, string) {
	n := 0
	for i := 0; i < len(s); {
		if l := escapeLen(s[i:]); l > 0 {
			i += l
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if n+runeWidth(r) > cols {
			return s[:i], s[i:]
		}
		n += runeWidth(r)
		i += size
	}
	return s, ""
}

// escapeLen is the length of the ANSI escape sequence at the start of s, or 0.
func escapeLen(s string) int {
	if len(s) < 2 || s[0] != 0x1b {
		return 0
	}
	if s[1] != '[' {
		return 2
	}
	for i := 2; i < len(s); i++ {
		if s[i] >= 0x40 && s[i] <= 0x7e {
			return i + 1
		}
	}
	return len(s)
}

// runeWidth counts East Asian wide characters and emoji as two columns, and
// control characters as none.
func runeWidth(r rune) int {
	switch {
	case r < 0x20 || r == 0x7f:
		return 0
	case r >= 0x1100 && r <= 0x115f, r >= 0x2e80 && r <= 0xa4cf, r >= 0xac00 && r <= 0xd7a3,
		r >= 0xf900 && r <= 0xfaff, r >= 0xfe30 && r <= 0xfe4f, r >= 0xff00 && r <= 0xff60,
		r >= 0xffe0 && r <= 0xffe6, r >= 0x1f300 && r <= 0x1f64f, r >= 0x1f900 && r <= 0x1f9ff,
		r >= 0x20000 && r <= 0x3fffd:
		return 2
	}
	return 1
}

func fitWidth(s string, w int) string {
	if visibleWidth(s) <= w {
		return s
	}
	head, _ := splitVisible(s, w-1)
	return head + "…"
}
