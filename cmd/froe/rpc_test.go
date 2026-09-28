package main

import (
	"strings"
	"testing"

	"github.com/dcoldeira/froe/internal/rpc"
)

// A selection from an unsaved buffer is named as one, not as a blank path.
func TestSelectionFromAnUnsavedBuffer(t *testing.T) {
	got := withEditorContext("can you see what I highlighted?",
		rpc.RunParams{Selection: "can you read this?", StartLine: 5, EndLine: 5})
	if strings.Contains(got, "of :") || !strings.Contains(got, "unsaved buffer") {
		t.Errorf("unsaved buffer not named:\n%s", got)
	}
}

// The text comes first, said to be present, and the request comes last.
func TestSelectionComesBeforeTheRequest(t *testing.T) {
	got := withEditorContext("explain this",
		rpc.RunParams{Selection: "def f(): pass", File: "src/a.py", StartLine: 3, EndLine: 3})
	sel, req := strings.Index(got, "def f(): pass"), strings.Index(got, "explain this")
	if sel < 0 || req < 0 || sel > req {
		t.Errorf("selection should precede the request:\n%s", got)
	}
	if !strings.Contains(got, "src/a.py, lines 3-3") || !strings.Contains(got, "no tool is needed") {
		t.Errorf("missing location or presence note:\n%s", got)
	}
	if !strings.HasSuffix(got, "explain this") {
		t.Errorf("request is not last:\n%s", got)
	}
}

func TestNoSelectionNamesTheFile(t *testing.T) {
	if got := withEditorContext("fix it", rpc.RunParams{File: "a.py"}); got != "fix it\n\n(The user is editing a.py.)" {
		t.Errorf("got %q", got)
	}
	if got := withEditorContext("fix it", rpc.RunParams{}); got != "fix it" {
		t.Errorf("got %q", got)
	}
}
