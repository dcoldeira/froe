package agent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/dcoldeira/froe/internal/perms"
	"github.com/dcoldeira/froe/internal/provider"
	"github.com/dcoldeira/froe/internal/registry"
	"github.com/dcoldeira/froe/internal/tools"
)

// alwaysAllow auto-approves, so a test exercises something other than the
// permission gate.
type alwaysAllow struct{}

func (alwaysAllow) Ask(perms.Request) perms.Decision { return perms.Allow }

func newTestAgent(t *testing.T, p provider.Provider) *Agent {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return &Agent{
		Provider: p,
		Model:    registry.Model{CtxMax: 8192, ToolStrategy: registry.ToolNative},
		Tools:    tools.NewRegistry(),
		Gate:     alwaysAllow{},
		Env:      tools.Env{Root: dir},
		MaxTurns: DefaultMaxTurns,
	}
}
