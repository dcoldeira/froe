package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/dcoldeira/froe/internal/provider"
	"github.com/dcoldeira/froe/internal/registry"
	"github.com/dcoldeira/froe/internal/tools"
)

// finalAnswer is a fake Provider that always answers immediately with text,
// no tool calls - enough to prove an image reached the wire without needing
// the full tool loop.
type finalAnswer struct{ gotImages int }

func (*finalAnswer) Name() string        { return "fake" }
func (*finalAnswer) Caps() provider.Caps { return provider.Caps{NativeTools: true, Vision: true} }

func (f *finalAnswer) Chat(ctx context.Context, req provider.Request) (<-chan provider.Event, error) {
	for _, m := range req.Messages {
		f.gotImages += len(m.Images)
	}
	ch := make(chan provider.Event, 2)
	ch <- provider.Event{Kind: provider.KindText, Text: "I see it"}
	ch <- provider.Event{Kind: provider.KindDone, Metrics: &provider.Metrics{}}
	close(ch)
	return ch, nil
}

// A model whose registry entry does not declare vision must refuse rather
// than silently send an image the backend cannot use - the failure needs to
// be visible immediately, not discovered later as "it just ignored the photo".
func TestRunRefusesImagesOnNonVisionModel(t *testing.T) {
	a := &Agent{
		Provider: &finalAnswer{},
		Model:    registry.Model{ID: "text-only", CtxMax: 8192, ToolStrategy: registry.ToolNative, Vision: false},
		Tools:    tools.NewRegistry(),
		Gate:     alwaysAllow{},
	}
	var lastErr error
	for ev := range a.Run(context.Background(), "what is this?", provider.ImageContent{MediaType: "image/png", Data: "Zm9v"}) {
		if ev.Kind == KindError {
			lastErr = ev.Err
		}
	}
	if lastErr == nil || !strings.Contains(lastErr.Error(), "does not support images") {
		t.Fatalf("got %v, want a vision-unsupported error", lastErr)
	}
}

func TestRunAttachesImagesOnVisionModel(t *testing.T) {
	fp := &finalAnswer{}
	a := &Agent{
		Provider: fp,
		Model:    registry.Model{ID: "vision-model", CtxMax: 8192, ToolStrategy: registry.ToolNative, Vision: true},
		Tools:    tools.NewRegistry(),
		Gate:     alwaysAllow{},
	}
	for range a.Run(context.Background(), "what is this?", provider.ImageContent{MediaType: "image/png", Data: "Zm9v"}) {
	}
	if fp.gotImages != 1 {
		t.Errorf("provider received %d images, want 1", fp.gotImages)
	}
}
