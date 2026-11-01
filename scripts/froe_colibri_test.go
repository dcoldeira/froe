package scripts_test

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// run calls froe-colibri with a fake coli, so no colibri is needed.
func run(t *testing.T, env []string, args ...string) (string, string, error) {
	t.Helper()
	cmd := exec.Command("bash", append([]string{"froe-colibri"}, args...)...)
	cmd.Env = append([]string{"PATH=/usr/bin:/bin", "HOME=" + t.TempDir(), "COLI=/fake/coli"}, env...)
	var out, errb strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	return out.String(), errb.String(), err
}

// The reason the script exists: COLI_PARK=0 is always set.
func TestDryRunAlwaysTurnsParkingOff(t *testing.T) {
	for _, gpu := range []string{"cuda", "vulkan", "cpu"} {
		out, _, err := run(t, nil, "--model", "/m", "--gpu", gpu, "--dry-run")
		if err != nil {
			t.Fatalf("%s: %v", gpu, err)
		}
		if !strings.HasPrefix(out, "COLI_PARK=0 ") {
			t.Errorf("%s: command does not start with COLI_PARK=0: %q", gpu, out)
		}
	}
}

func TestDryRunBackendAndArguments(t *testing.T) {
	out, _, err := run(t, nil, "--model", "/models/q", "--model-id", "qc", "--port", "8001",
		"--ctx", "16384", "--gpu", "cuda", "--log-reuse", "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"COLI_CUDA=1", "CUDA_EXPERT_GB=auto", "COLI_PREFIX_LOG=1",
		"/fake/coli serve", "--model /models/q", "--model-id qc", "--port 8001", "--ctx 16384", "--auto-tier"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in %q", want, out)
		}
	}
	vk, _, _ := run(t, nil, "--model", "/m", "--gpu", "vulkan", "--dry-run")
	if !strings.Contains(vk, "COLI_VULKAN=1") || strings.Contains(vk, "COLI_CUDA") {
		t.Errorf("vulkan: %q", vk)
	}
	cpu, _, _ := run(t, nil, "--model", "/m", "--gpu", "cpu", "--dry-run")
	if strings.Contains(cpu, "COLI_CUDA") || strings.Contains(cpu, "COLI_VULKAN") {
		t.Errorf("cpu: %q", cpu)
	}
}

// Off the default port, it says what froe's config needs.
func TestOtherPortNamesTheBaseURL(t *testing.T) {
	_, stderr, err := run(t, nil, "--model", "/m", "--port", "8001", "--gpu", "cpu", "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr, `base_url = "http://127.0.0.1:8001/v1"`) {
		t.Errorf("no base_url hint: %q", stderr)
	}
}

func TestRefusesBadInput(t *testing.T) {
	cases := [][]string{
		{"--dry-run"}, // no model
		{"--model", "/m", "--gpu", "tpu", "--dry-run"}, // unknown backend
		{"--model", "/m", "--bogus"},                   // unknown flag
		{"--model", "/does/not/exist", "--gpu", "cpu"}, // missing dir, real run
	}
	for _, args := range cases {
		if _, _, err := run(t, nil, args...); err == nil {
			t.Errorf("%v: accepted", args)
		}
	}
}

func TestModelFromEnvironment(t *testing.T) {
	out, _, err := run(t, []string{"FROE_COLIBRI_MODEL=/env/model", "FROE_COLIBRI_PORT=9000"},
		"--gpu", "cpu", "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "--model /env/model") || !strings.Contains(out, "--port 9000") {
		t.Errorf("environment defaults not used: %q", out)
	}
}

// serveModels answers /v1/models the way colibri does, with the given model
// id, or with a 404 when id is empty (some other service on the port).
func serveModels(t *testing.T, id string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if id == "" || r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprintf(w, `{"object":"list","data":[{"id":"%s","owned_by":"colibri"}]}`, id)
	}))
	t.Cleanup(srv.Close)
	return srv.URL[strings.LastIndex(srv.URL, ":")+1:]
}

func background(t *testing.T, env []string, port string) (string, error) {
	t.Helper()
	env = append(env, "FROE_COLIBRI_LOG="+filepath.Join(t.TempDir(), "colibri.log"), "FROE_COLIBRI_WAIT=20")
	_, stderr, err := run(t, env, "--model", t.TempDir(), "--port", port, "--gpu", "cpu", "--background")
	return stderr, err
}

// <leader>dl twice must not start a second server.
func TestBackgroundAlreadyServing(t *testing.T) {
	stderr, err := background(t, nil, serveModels(t, "qwen3-coder-30b"))
	if err != nil || !strings.Contains(stderr, "already serving qwen3-coder-30b") {
		t.Fatalf("err %v, stderr %q", err, stderr)
	}
}

func TestBackgroundRefusesAnotherModel(t *testing.T) {
	stderr, err := background(t, nil, serveModels(t, "flint"))
	if err == nil || !strings.Contains(stderr, "another model") {
		t.Fatalf("err %v, stderr %q", err, stderr)
	}
}

// Port 8000 on the reference laptop belongs to an unrelated web app.
func TestBackgroundRefusesAPortTakenBySomethingElse(t *testing.T) {
	stderr, err := background(t, nil, serveModels(t, ""))
	if err == nil || !strings.Contains(stderr, "not colibri") {
		t.Fatalf("err %v, stderr %q", err, stderr)
	}
}

// fakeColi writes a coli that serves /v1/models on its --port, or dies.
func fakeColi(t *testing.T, dies bool) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "coli")
	body := `#!/usr/bin/env bash
while [ $# -gt 0 ]; do case "$1" in --port) port="$2"; shift 2;; --model-id) id="$2"; shift 2;; *) shift;; esac; done
`
	if dies {
		body += "echo 'fake coli: cannot load the model'; exit 1\n"
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

func freePort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return fmt.Sprint(l.Addr().(*net.TCPAddr).Port)
}

func TestBackgroundStartsAndWaitsUntilReady(t *testing.T) {
	port := freePort(t)
	stderr, err := background(t, []string{"COLI=" + fakeColi(t, false)}, port)
	if m := regexp.MustCompile(`pid (\d+)`).FindStringSubmatch(stderr); m != nil {
		t.Cleanup(func() { exec.Command("kill", m[1]).Run() })
	}
	if err != nil || !strings.Contains(stderr, "ready, serving qwen3-coder-30b") {
		t.Fatalf("err %v, stderr %q", err, stderr)
	}
}

func TestBackgroundReportsAStartThatDies(t *testing.T) {
	stderr, err := background(t, []string{"COLI=" + fakeColi(t, true)}, freePort(t))
	if err == nil || !strings.Contains(stderr, "cannot load the model") {
		t.Fatalf("err %v, stderr %q", err, stderr)
	}
}
