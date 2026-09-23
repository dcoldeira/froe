package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExtractImagePathsHandlesEscapedSpaces(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "Screenshot From 2026-09-12 18-29-59.png")
	if err := os.WriteFile(real, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Exactly the shell-escaping habit seen in the transcript: a real path
	// typed with backslash-escaped spaces into a REPL that is not a shell.
	escaped := filepath.Join(dir, `Screenshot\ From\ 2026-09-12\ 18-29-59.png`)

	got := extractImagePaths("can you see this " + escaped)
	if len(got) != 1 || got[0] != real {
		t.Fatalf("got %v, want [%q]", got, real)
	}
}

func TestExtractImagePathsIgnoresMentionsOfMissingFiles(t *testing.T) {
	got := extractImagePaths("compare this to /tmp/does-not-exist-xyz.png please")
	if len(got) != 0 {
		t.Errorf("got %v, want none - the path does not exist", got)
	}
}

func TestExtractImagePathsIgnoresNonImageFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "notes.txt")
	os.WriteFile(path, []byte("x"), 0o644)

	got := extractImagePaths("read " + path)
	if len(got) != 0 {
		t.Errorf("got %v, want none - not an image extension", got)
	}
}

func TestLoadAttachedImagesReturnsNilWithNoPaths(t *testing.T) {
	images, err := loadAttachedImages("just a normal question, no files")
	if err != nil {
		t.Fatal(err)
	}
	if images != nil {
		t.Errorf("got %v, want nil for a message with nothing to attach", images)
	}
}
