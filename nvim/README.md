# froe.nvim

A thin Neovim client for the `froe` binary. It speaks newline-delimited
JSON-RPC over stdio — the LSP pattern — so the editor and the terminal share one
implementation, one session store and one set of tools.

**The plugin holds no agent logic.** It is a transport and a renderer. If it
grows past ~500 lines, something has leaked out of the binary
(`docs/ARCHITECTURE.md` §8). Currently ~410.

## Install

The binary must be on `$PATH`:

```bash
go install github.com/dcoldeira/froe@latest
```

Then with lazy.nvim — put this in your own dotfiles, not in this repo:

```lua
-- ~/.config/nvim/lua/plugins/froe.lua
return {
  "dcoldeira/froe",
  -- the Lua lives in nvim/ inside the repo
  config = function(plugin)
    vim.opt.runtimepath:append(plugin.dir .. "/nvim")
    require("froe").setup({
      cmd = "froe",
      mode = "ask",          -- "ask" | "accept-edits" | "yolo"
      show_reasoning = false,
    })
  end,
}
```

For a local checkout, swap the spec for `dir = "~/development/froe"`.

## Commands

| Command | Does |
|---|---|
| `:Froe <task>` | run a task in the project |
| `:FroeVisual <task>` | run a task scoped to the visual selection |
| `:FroeStop` | cancel the running task |
| `:FroeOpen` / `:FroeClose` | show or hide the output split |
| `:FroeClear` | clear the output |
| `:FroeRestart` | restart the backing process |

Default keymaps (disable with `keys = false`):

- `<leader>dd` — task (normal), task on selection (visual)
- `<leader>ds` — stop
- `<leader>do` — open output

## Approvals

Mutating tools prompt through `vim.fn.confirm` — Allow / Deny / Always. The Go
side genuinely blocks on the answer, so a dialog you ignore stalls the run
rather than silently proceeding. An editor that cannot answer is treated as a
denial, never as consent.

`mode = "accept-edits"` auto-approves file edits but still asks before shell
commands. `mode = "yolo"` approves everything the hard denylist has not already
refused.

## What it shares with the CLI

Sessions, project memory and the repo map are the same store, so a conversation
started in the terminal is visible to `froe sessions`, and a fact learned in
Neovim is remembered on the command line.
