// Package config resolves where froe keeps its configuration.
package config

import (
	"os"
	"path/filepath"
	"strings"
)

// Dir returns the froe config directory, honouring XDG_CONFIG_HOME.
// It does not create the directory — absence is a valid state, since every
// file in it is an optional overlay on the embedded defaults.
func Dir() string {
	if d := os.Getenv("FROE_CONFIG_DIR"); d != "" {
		return d
	}
	if d := os.Getenv("XDG_CONFIG_HOME"); d != "" {
		return filepath.Join(d, "froe")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".froe"
	}
	return filepath.Join(home, ".config", "froe")
}

// ExpandUser resolves a leading ~ in a path. Config files are hand-edited, so
// tilde paths are expected rather than exceptional.
func ExpandUser(p string) string {
	if p == "" || !strings.HasPrefix(p, "~") {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return p
	}
	if p == "~" {
		return home
	}
	if strings.HasPrefix(p, "~/") {
		return filepath.Join(home, p[2:])
	}
	return p // ~user form is not supported
}
