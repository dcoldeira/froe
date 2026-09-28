# froe.nvim

A thin Neovim client for the `froe` binary. It speaks newline-delimited
JSON-RPC over stdio — the LSP pattern — so the editor and the terminal share one
implementation, one session store and one set of tools.

**The plugin holds no agent logic.** It is a transport and a renderer. If it
grows past ~500 lines, something has leaked out of the binary
(`docs/ARCHITECTURE.md` §8). Currently ~550, so it is due a trim.

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
      -- Optional: what :FroeLaunch runs to bring the model up. Machine-specific.
      launch = { "sh", "-c", "lms unload --all && lms load mistralai/ministral-3-8b"
        .. " --gpu max --context-length 8192 --parallel 1 -y" },
    })
  end,
}
```

For a local checkout, swap the spec for `dir = "~/development/froe"`. After
rebuilding the binary, run `:FroeRestart` so the plugin starts the new one.

## Using it

1. Open Neovim in the project: `cd your-project && nvim .`
2. `<leader>dl` (`:FroeLaunch`) brings the model up, if you configured `launch`.
3. `<leader>dd` puts `:Froe ` on the command line. Type the task and press
   Enter, e.g. `:Froe where is the witness value computed?`
4. The answer streams into an output split on the right. If you do not see it,
   `<leader>do` (`:FroeOpen`) brings it back.

**The task goes on the command line, not into a buffer.** froe is not a chat
window you type into; the output split is read-only.

**To ask about specific lines**, select them (`V` and a motion), press
`<leader>dd`, and type the question. The selected text is sent with it, so this
works on an unsaved buffer too. Without a selection, froe only sees files on
disk: save (`:w`) before asking about a file you are editing.

**Long prompts: copy, then `<leader>dp`.** Neovim keeps only the first line of
a multi-line paste into the command line, so a prompt that wrapped when you
copied it reaches froe cut short, silently. `:FroePaste` reads the clipboard
instead and joins the lines.

**One task at a time.** A second `:Froe` while one runs is refused; `<leader>ds`
stops the current one.

## Commands

| Command | Does |
|---|---|
| `:Froe <task>` | run a task in the project |
| `:FroeVisual <task>` | run a task scoped to the visual selection |
| `:FroePaste` | run the task on the system clipboard, lines joined |
| `:FroeStop` | cancel the running task |
| `:FroeOpen` / `:FroeClose` | show or hide the output split |
| `:FroeClear` | clear the output |
| `:FroeLaunch` | bring the model runtime up (runs `launch` from your config) |
| `:FroeModel [id]` | show the model, or switch to another registry id (restarts the backend) |
| `:FroeRestart` | restart the backing process, e.g. after rebuilding `froe` |

Default keymaps (disable with `keys = false`):

- `<leader>dd` — task (normal), task on selection (visual)
- `<leader>dp` — task from the clipboard
- `<leader>dl` — launch the model runtime
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
