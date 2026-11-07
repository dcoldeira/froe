# Setting up froe on a new machine

Steps to go from a bare machine to `froe` working in the terminal and in
Neovim, with a local model on an open-source runtime. The reference machine is
a laptop with an RTX 4060 (8 GB VRAM) and 16 GB of RAM.

llama.cpp is the default runtime (since 2026-11-06), serving Qwen3-Coder-30B-A3B.
colibri is supported as an alternative (§4b).

---

## 1. Clone

```bash
gh repo clone dcoldeira/froe
cd froe
```

## 2. Go toolchain

```bash
sudo apt install golang-go   # or the tarball from go.dev if you need a newer version
go version
```

`go.mod` pins a toolchain version; `go build`/`go install` will auto-download
it on first run if the system Go is older. That's normal, not an error.

## 3. Build and install the binary

```bash
go install ./cmd/froe
```

Installs to `$(go env GOPATH)/bin/froe` — normally `~/go/bin/froe`. Add
that to `PATH` (in `~/.bashrc`):

```bash
export PATH="$PATH:$HOME/go/bin"
```

(`make install` is the alternative path — installs to `~/.local/bin` instead.
Either is fine; don't do both or you'll have two binaries shadowing each
other. `which froe` to check if unsure.)

## 4. Model runtime: llama.cpp

[llama.cpp](https://github.com/ggml-org/llama.cpp) (MIT) is the default runtime.
Its `llama-server` speaks the OpenAI API, keeps the conversation it has read so
each turn reads only what is new, and splits a mixture-of-experts model between
the GPU and the CPU to fit the card.

**Build llama.cpp** with your GPU's backend. On an NVIDIA card with the CUDA
toolkit installed (froe's start script looks in `~/src/llama.cpp` if
`llama-server` is not on `PATH`):

```bash
git clone https://github.com/ggml-org/llama.cpp ~/src/llama.cpp
cd ~/src/llama.cpp
cmake -B build -DGGML_CUDA=ON -DCMAKE_BUILD_TYPE=Release -DLLAMA_CURL=OFF
cmake --build build --config Release -j --target llama-server
```

Without CUDA, leave out `-DGGML_CUDA=ON` (or use `-DGGML_VULKAN=ON`). Release
binaries for Linux, macOS and Windows are also published on the llama.cpp
releases page. The measurements in this repo used release `b12169`.

**Get the default model**, Qwen3-Coder-30B-A3B-Instruct at Q4_K_M (18.6 GB),
from Hugging Face (`unsloth/Qwen3-Coder-30B-A3B-Instruct-GGUF`), to the path the
registry expects:

```bash
mkdir -p ~/models
curl -L -o ~/models/Qwen3-Coder-30B-A3B-Instruct-Q4_K_M.gguf \
  https://huggingface.co/unsloth/Qwen3-Coder-30B-A3B-Instruct-GGUF/resolve/main/Qwen3-Coder-30B-A3B-Instruct-Q4_K_M.gguf
```

**Start it with `froe-llamacpp`** (installed next to froe by `make install`). It
sets what froe needs: one slot (`--parallel 1`, so a conversation stays where it
was read), the model's own chat template (`--jinja`, for native tool calls) and
a 32K window (`--ctx` changes it). llama.cpp's `--fit` sizes the GPU/CPU split
for that window: on an 8 GB card, 8K to 64K measured the same speed.

```bash
froe-llamacpp --model ~/models/Qwen3-Coder-30B-A3B-Instruct-Q4_K_M.gguf               # foreground
froe-llamacpp --model ~/models/Qwen3-Coder-30B-A3B-Instruct-Q4_K_M.gguf --background  # detached; returns when ready
```

Anything after `--` goes to llama-server as is, for example
`-- --n-cpu-moe 34` to set the split by hand.

**You rarely need to run it yourself.** When llama-server is down, froe starts it
with `froe-llamacpp --background` before the first request (the runtime's `start`
command, using the model's `path` from the registry) and says so on stderr; in
Neovim that line is a notification. `--background` does nothing if the model is
already serving, and logs to `~/.cache/froe/llamacpp.log`. Neovim's `<leader>dl`
can run it too, to start it ahead of time (§6). If port 8080 is taken, pass
`--port` and set the same `base_url` under `[runtime.llamacpp]` in
`~/.config/froe/runtimes.toml`. An entry there replaces the shipped one
outright, so copy every field, including `serves_one = true`,
`prefix_reuse = true` and the `start` line. If the model file is elsewhere,
override the model's `path` in `~/.config/froe/models.toml`.

Check it: `froe doctor` should list llamacpp as running and the model as
`serving`.

**What to expect on an 8 GB GPU with 16 GB of RAM** (measured 2026-11-06, RTX
4060 laptop): about 7 GB of VRAM, ~22-35 tokens a second generated and ~200-425
read, and first turns of 0-14 s. Filling the whole 32K window cold takes a couple
of minutes; later turns of the same conversation read only what is new. The experts that stay on the CPU are read from the
file through the page cache, which takes most of the RAM (~12 GB). Close other
heavy programs while it runs; if the system runs short of memory, the model
pages experts back in from disk and slows down rather than failing. See
`eval/README.md` for the full numbers.

## 4b. Alternative runtime: colibri

[colibri](https://github.com/JustVugg/colibri) (Apache 2.0) runs
mixture-of-experts models by keeping the active part in RAM and streaming the
experts from disk. On an 8 GB card it is about six times slower than llama.cpp
with the same model (16/30 on the eval in 63 minutes, against 22/30 in 11), so it
is not the default; it is worth trying on machines it fits better. Install it from
its README (`./start-here.sh`), import the same GGUF, and start it with
`froe-colibri`, which sets `COLI_PARK=0` and the GPU backend:

```bash
coli import ~/models/Qwen3-Coder-30B-A3B-Instruct-Q4_K_M.gguf --out ~/colibri-models/qwen3-coder-30b-gguf --mode link
froe-colibri --model ~/colibri-models/qwen3-coder-30b-gguf --background
froe do --model qwen3-coder-30b-colibri "..."
```

**Other open-source runtimes** work through the same adapter, such as Ollama
(set `num_ctx`; its 4096 default silently truncates). `froe commit` uses a small model on
Ollama:

```bash
ollama pull qwen2.5-coder:1.5b
```

## 5. Machine profile

`froe doctor` detects hardware and reports what's runnable, but does not
yet persist a profile automatically (aspirational per the README — worth
revisiting). Write one by hand at
`~/.config/froe/machines/<hostname>.toml`:

```toml
[hardware]
gpu     = "<from `froe doctor`>"
vram_mb = <from `froe doctor`>
ram_mb  = <from `froe doctor`>
threads = <from `froe doctor`>

[roles]
fast  = "qwen2.5-coder:1.5b"     # commit messages, classification
main  = "<id from froe doctor>"  # everyday editing
heavy = "<id from froe doctor>"  # multi-file reasoning
```

Run `froe doctor` first — it prints exactly these hardware numbers and a
fit table telling you which registry models this box can actually run.
Model IDs come from
`internal/registry/defaults/models.toml` — don't invent new ones for an
existing model, reuse the id doctor already lists.

Sanity-check:

```bash
froe doctor                       # confirm the profile is picked up
froe ask -model <id> "one sentence: what is a goroutine?"
```

The output line (`N→M tokens (R reasoning) · X tok/s · ttft Ys · total Zs`)
is the number to compare across machines.

## 5b. Web search (optional)

The `web_search` tool needs a Tavily API key — get one free at
[tavily.com](https://tavily.com) (1,000 credits/month) and export it:

```bash
export TAVILY_API_KEY="tvly-..."   # add to ~/.bashrc
```

Without it the tool is still registered (so the model can discover it exists)
but every call fails with a message naming the missing env var, rather than
froe refusing to start. Like `bash`, it prompts for approval before running
— it only ever sends the search query outward, never file contents, but it is
still traffic leaving the machine, which the rest of froe's toolset
deliberately never does (see the README's "why it exists" #1).

## 6. Neovim plugin

The plugin ships **inside this repo**, at `nvim/` — there is no separate
plugin repo to clone. Wire it into your Neovim config as a local-checkout
lazy.nvim spec:

```lua
-- ~/.config/nvim/lua/plugins/froe.lua
return {
  "dcoldeira/froe",
  dir = "~/development/froe",   -- wherever you cloned it on THIS machine
  config = function(plugin)
    vim.opt.runtimepath:append(plugin.dir .. "/nvim")
    require("froe").setup({
      cmd = "froe",
      model = "",                     -- "" lets the router pick per task, or pin a registry id
      mode = "ask",                   -- "ask" | "accept-edits" | "yolo"
      show_reasoning = false,
    })
  end,
}
```

**Keep this file untracked in your dotfiles repo** — the `dir` path is
machine-specific (a different checkout location on another box breaks the
spec), so committing it defeats the point of dotfiles being portable. Add it,
don't `git add` it.

Restart Neovim (or `:Lazy sync` if lazy.nvim doesn't pick up a new file spec
on its own) and confirm:

```vim
:Froe what does this file do?
```

See `nvim/README.md` for the full command/keymap reference and how approvals
work.

## 7. What to re-measure per machine

Hardware varies enough that these are worth re-checking on every new box
rather than assumed from another machine's numbers (`docs/MODELS.md` §1 has
the reasoning — quantisation class and prefill dominate, not raw params):

- `froe doctor`'s fit table — which registry models actually run here
- a real `froe ask` timing, not the doctor's `~peak` estimate
- GPU offload — confirm how much of the model is on the GPU (`nvidia-smi`
  during a generation) rather than assuming it
