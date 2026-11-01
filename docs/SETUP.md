# Setting up froe on a new machine

Steps to go from a bare machine to `froe` working in the terminal and in
Neovim, with a local model on an open-source runtime. The reference machine is
a laptop with an RTX 4060 (8 GB VRAM) and 16 GB of RAM.

Colibri is the default runtime (since 2026-11-01). LM Studio is closed source
and no longer used by default; it still works if you point froe at it.

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

## 4. Model runtime: colibri

[colibri](https://github.com/JustVugg/colibri) (Apache 2.0) runs
mixture-of-experts models on ordinary machines by keeping the active part in RAM
and streaming the experts from disk. It reads a prompt it has seen before almost
for free, and froe keeps its conversations append-only so every turn after the
first costs only what is new.

**Install colibri** (Linux, macOS, Windows): follow its README; the one-step
setup is `./start-here.sh` (or `START-HERE.bat`). On an NVIDIA card with the
CUDA toolkit installed, build the engine with CUDA; Vulkan works on any GPU.

**Get the default model**, Qwen3-Coder-30B-A3B, from a published GGUF:

```bash
coli import Qwen3-Coder-30B-A3B-Instruct-Q4_K_M.gguf --out ~/colibri-models/qwen3-coder-30b-gguf --mode link
```

**Start it with `froe-colibri`** (installed next to froe by `make install`). It
sets what froe needs: `COLI_PARK=0` (otherwise a new task shorter than the
conversation colibri holds is taken for a side question and read again every
turn) and the GPU backend.

```bash
froe-colibri --model ~/colibri-models/qwen3-coder-30b-gguf               # foreground
froe-colibri --model ~/colibri-models/qwen3-coder-30b-gguf --background  # detached; returns when ready
```

**You rarely need to run it yourself.** When colibri is down, froe starts it with
`froe-colibri --background` before the first request (the runtime's `start`
command, using the model's `path` from the registry) and says so on stderr; in
Neovim that line is a notification. `--background` does nothing if the model is
already serving, and logs to `~/.cache/froe/colibri.log`. Neovim's `<leader>dl`
can run it too, to start colibri ahead of time (§6). If port
8000 is taken, pass `--port` and set the same `base_url` under
`[runtime.colibri]` in `~/.config/froe/runtimes.toml`. An entry there replaces
the shipped one outright, so copy every field, including `serves_one = true`
`prefix_reuse = true` and the `start` line. The model's location is its `path`
in the registry (`~/colibri-models/qwen3-coder-30b-gguf` for the default);
override it in `~/.config/froe/models.toml` if yours is elsewhere.

Check it: `froe doctor` should list colibri as running and the model as
`serving`.

**On a smaller machine** the first turn of a project is slow (about 50-150 s on
an 8 GB GPU with 16 GB of RAM, cold) and generation is about 5 tokens a second;
the turns after it, and later commands in the same project, are fast. See
`eval/README.md` for measured numbers.

**Other open-source runtimes** work through the same adapter: llama.cpp
(`llama-server --jinja -c 8192`, with GBNF grammars) and Ollama (set `num_ctx`;
its 4096 default silently truncates). `froe commit` uses a small model on
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
