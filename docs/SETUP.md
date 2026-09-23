# Setting up froe on a new machine

Steps to go from a bare machine to `froe` working in the terminal and in
Neovim, with a local model served through LM Studio. Followed end-to-end on
`tufa` (RTX 4060, 8 GB VRAM) 2026-09-12.

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

## 4. Pick a model backend

Froe talks OpenAI-compatible `/v1/chat/completions`, so any of Ollama,
LM Studio, or llama.cpp work. LM Studio is the path for Bonsai (see
`docs/MODELS.md` §5 for why). To set it up headless:

```bash
# LM Studio ships its own CLI at ~/.lmstudio/bin/lms
export PATH="$PATH:$HOME/.lmstudio/bin"   # add to ~/.bashrc too

lms daemon up            # starts the llmster daemon, no GUI needed
lms server status        # confirm it's listening on :1234
lms get prism-ml/bonsai-27b -y --gguf   # pulls the plain Q1_0 build if not already local
```

Load the model **with explicit flags** — LM Studio's auto-defaults pick
`--parallel 4`, which reserves KV-cache slots for concurrency a single
interactive user doesn't need and costs both VRAM and per-request speed:

```bash
lms load "prism-ml/bonsai-27b" --gpu max -c 8192 --parallel 1 --ttl 86400 -y
```

Verify with `lms ps` — should read `PARALLEL 1`, `CONTEXT 8192`,
`TTL 24h / 24h`. Because the TTL means the model unloads after 24h idle,
**re-check `lms ps` after any long idle period or reboot** — a plain
`lms load` with no flags after that will silently regress to `parallel 4`.

If you're on Ollama instead for a role (e.g. the small `fast`-role model):

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
main  = "bonsai-27b-lmstudio"    # everyday editing, if VRAM allows
heavy = "bonsai-27b-lmstudio"    # multi-file reasoning
```

Run `froe doctor` first — it prints exactly these hardware numbers and a
fit table telling you which registry models this box can actually run.
Model IDs (`bonsai-27b-lmstudio`, etc.) come from
`internal/registry/defaults/models.toml` — don't invent new ones for an
existing model, reuse the id doctor already lists.

Sanity-check:

```bash
froe doctor                       # confirm the profile is picked up
froe ask -model bonsai-27b-lmstudio "one sentence: what is a goroutine?"
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
      model = "bonsai-27b-lmstudio",  -- or "" to let the router pick per-task
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
- GPU offload — confirm the model is fully VRAM-resident (`lms ps`, or
  `nvidia-smi` during a generation) rather than partially spilling to CPU
