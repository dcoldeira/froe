package main

import (
	"io"
	"os"
)

// style applies ANSI colour when the destination is a terminal.
type style struct{ on bool }

func newStyle(w io.Writer) style {
	if os.Getenv("NO_COLOR") != "" {
		return style{}
	}
	f, ok := w.(*os.File)
	if !ok {
		return style{}
	}
	info, err := f.Stat()
	if err != nil {
		return style{}
	}
	return style{on: info.Mode()&os.ModeCharDevice != 0}
}

func (s style) wrap(code, text string) string {
	if !s.on {
		return text
	}
	return "\033[" + code + "m" + text + "\033[0m"
}

func (s style) bold(t string) string   { return s.wrap("1", t) }
func (s style) dim(t string) string    { return s.wrap("2", t) }
func (s style) yellow(t string) string { return s.wrap("33", t) }
