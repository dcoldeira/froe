package provider

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadImageFileReadsAndEncodes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "shot.png")
	raw := []byte("not a real PNG, just bytes to round-trip")
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}

	img, err := LoadImageFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if img.MediaType != "image/png" {
		t.Errorf("media type = %q", img.MediaType)
	}
	got, err := base64.StdEncoding.DecodeString(img.Data)
	if err != nil {
		t.Fatalf("Data is not valid base64: %v", err)
	}
	if string(got) != string(raw) {
		t.Errorf("round-trip mismatch: got %q, want %q", got, raw)
	}
}

func TestLoadImageFileRejectsUnsupportedExtension(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "notes.txt")
	os.WriteFile(path, []byte("hi"), 0o644)

	_, err := LoadImageFile(path)
	if err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("got %v, want an unsupported-type error", err)
	}
}

func TestLoadImageFileReportsMissingFile(t *testing.T) {
	_, err := LoadImageFile("/does/not/exist.png")
	if err == nil {
		t.Fatal("expected an error for a missing file")
	}
}

func TestLoadImageFileRejectsDirectory(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "shot.png")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := LoadImageFile(sub)
	if err == nil || !strings.Contains(err.Error(), "directory") {
		t.Fatalf("got %v, want a directory error", err)
	}
}
