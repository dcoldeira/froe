package scripts_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// runLlama calls froe-llamacpp with a fake llama-server, so no llama.cpp is needed.
func runLlama(t *testing.T, env []string, args ...string) (string, string, error) {
	t.Helper()
	cmd := exec.Command("bash", append([]string{"froe-llamacpp"}, args...)...)
	cmd.Env = append([]string{"PATH=/usr/bin:/bin", "HOME=" + t.TempDir(), "LLAMA_SERVER=/fake/llama-server"}, env...)
	var out, errb strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	return out.String(), errb.String(), err
}

// The flags froe depends on are always there: the model's own template for
// native tool calls, and one slot so a conversation stays where it was read.
func TestLlamaDryRunFlags(t *testing.T) {
	out, _, err := runLlama(t, nil, "--model", "/m/q.gguf", "--model-id", "qc", "--port", "8090",
		"--ctx", "16384", "--dry-run", "--", "--n-cpu-moe", "34")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"/fake/llama-server -m /m/q.gguf", "--alias qc", "--jinja",
		"-c 16384", "--parallel 1", "--port 8090", "--n-cpu-moe 34"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in %q", want, out)
		}
	}
}

func TestLlamaOtherPortNamesTheBaseURL(t *testing.T) {
	_, stderr, err := runLlama(t, nil, "--model", "/m.gguf", "--port", "8090", "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr, `base_url = "http://127.0.0.1:8090/v1"`) {
		t.Errorf("no base_url hint: %q", stderr)
	}
}

func TestLlamaRefusesBadInput(t *testing.T) {
	cases := [][]string{
		{"--dry-run"},                       // no model
		{"--model", "/m.gguf", "--bogus"},   // unknown flag
		{"--model", "/does/not/exist.gguf"}, // missing file, real run
	}
	for _, args := range cases {
		if _, _, err := runLlama(t, nil, args...); err == nil {
			t.Errorf("%v: accepted", args)
		}
	}
}

func TestLlamaModelFromEnvironment(t *testing.T) {
	out, _, err := runLlama(t, []string{"FROE_LLAMACPP_MODEL=/env/m.gguf", "FROE_LLAMACPP_PORT=9000"}, "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "-m /env/m.gguf") || !strings.Contains(out, "--port 9000") {
		t.Errorf("environment defaults not used: %q", out)
	}
}

// serveLlamaModels answers /v1/models the way llama-server does (compact JSON,
// the alias as the id), or 404s when id is empty (some other service).
func serveLlamaModels(t *testing.T, id string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if id == "" || r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprintf(w, `{"object":"list","data":[{"id":"%s","owned_by":"llamacpp"}]}`, id)
	}))
	t.Cleanup(srv.Close)
	return srv.URL[strings.LastIndex(srv.URL, ":")+1:]
}

func llamaBackground(t *testing.T, env []string, port string) (string, error) {
	t.Helper()
	model := filepath.Join(t.TempDir(), "m.gguf")
	if err := os.WriteFile(model, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	env = append(env, "FROE_LLAMACPP_LOG="+filepath.Join(t.TempDir(), "llamacpp.log"), "FROE_LLAMACPP_WAIT=20")
	_, stderr, err := runLlama(t, env, "--model", model, "--port", port, "--background")
	return stderr, err
}

func TestLlamaBackgroundAlreadyServing(t *testing.T) {
	stderr, err := llamaBackground(t, nil, serveLlamaModels(t, "qwen3-coder-30b"))
	if err != nil || !strings.Contains(stderr, "already serving qwen3-coder-30b") {
		t.Fatalf("err %v, stderr %q", err, stderr)
	}
}

func TestLlamaBackgroundRefusesAnotherModel(t *testing.T) {
	stderr, err := llamaBackground(t, nil, serveLlamaModels(t, "ministral-3-8b"))
	if err == nil || !strings.Contains(stderr, "another model") {
		t.Fatalf("err %v, stderr %q", err, stderr)
	}
}

func TestLlamaBackgroundRefusesAPortTakenBySomethingElse(t *testing.T) {
	stderr, err := llamaBackground(t, nil, serveLlamaModels(t, ""))
	if err == nil || !strings.Contains(stderr, "not llama-server") {
		t.Fatalf("err %v, stderr %q", err, stderr)
	}
}

// fakeLlamaServer writes a llama-server that serves /v1/models on its --port
// under its --alias, or dies.
func fakeLlamaServer(t *testing.T, dies bool) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "llama-server")
	body := `#!/usr/bin/env bash
while [ $# -gt 0 ]; do case "$1" in --port) port="$2"; shift 2;; --alias) id="$2"; shift 2;; *) shift;; esac; done
`
	if dies {
		body += "echo 'fake llama-server: failed to load model'; exit 1\n"
	} else {
		body += `exec python3 -c '
import http.server, sys
port, mid = int(sys.argv[1]), sys.argv[2]
class H(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        b = ("{\"data\":[{\"id\":\"%s\"}]}" % mid).encode()
        self.send_response(200); self.send_header("Content-Length", str(len(b))); self.end_headers(); self.wfile.write(b)
    def log_message(self, *a): pass
http.server.HTTPServer(("127.0.0.1", port), H).serve_forever()' "$port" "$id"
`
	}
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLlamaBackgroundStartsAndWaitsUntilReady(t *testing.T) {
	stderr, err := llamaBackground(t, []string{"LLAMA_SERVER=" + fakeLlamaServer(t, false)}, freePort(t))
	if m := regexp.MustCompile(`pid (\d+)`).FindStringSubmatch(stderr); m != nil {
		t.Cleanup(func() { exec.Command("kill", m[1]).Run() })
	}
	if err != nil || !strings.Contains(stderr, "ready, serving qwen3-coder-30b") {
		t.Fatalf("err %v, stderr %q", err, stderr)
	}
}

func TestLlamaBackgroundReportsAStartThatDies(t *testing.T) {
	stderr, err := llamaBackground(t, []string{"LLAMA_SERVER=" + fakeLlamaServer(t, true)}, freePort(t))
	if err == nil || !strings.Contains(stderr, "failed to load model") {
		t.Fatalf("err %v, stderr %q", err, stderr)
	}
}
