package main

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/dcoldeira/froe/internal/provider"
)

// imagePathPattern matches a filesystem path ending in a known image
// extension. `(?:[^\s\\]|\\.)+` allows a run of ordinary characters OR a
// backslash-escaped one, so a shell-style escaped space ("Screenshot\ From")
// - which is how a path pasted from a terminal habit looks, even though this
// is not a shell - still reads as one token instead of breaking at the space.
var imagePathPattern = regexp.MustCompile(`(?i)(?:[^\s\\]|\\.)+\.(?:png|jpe?g|gif|webp)`)

// extractImagePaths finds candidate image paths in free-form text and returns
// only the ones that actually exist on disk, unescaped. A path mentioned but
// not found is silently skipped rather than errored - the user may just be
// talking about a file, not attaching one, and guessing wrong should not
// block the message from being sent as plain text.
func extractImagePaths(text string) []string {
	var found []string
	for _, raw := range imagePathPattern.FindAllString(text, -1) {
		path := strings.NewReplacer(`\ `, " ", `\\`, `\`).Replace(raw)
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			found = append(found, path)
		}
	}
	return found
}

// loadAttachedImages resolves every image path found in line. Returns nil,
// nil when there is nothing to attach - the common case - so callers can
// treat "no images" as free.
func loadAttachedImages(line string) ([]provider.ImageContent, error) {
	paths := extractImagePaths(line)
	if len(paths) == 0 {
		return nil, nil
	}
	images := make([]provider.ImageContent, 0, len(paths))
	for _, p := range paths {
		img, err := provider.LoadImageFile(p)
		if err != nil {
			return nil, fmt.Errorf("attaching %s: %w", p, err)
		}
		images = append(images, img)
	}
	return images, nil
}
