-- froe.nvim — a thin client for the froe binary.
--
-- Everything the agent does happens in Go. This file wires commands to RPC
-- calls and pipes events into a buffer. If it starts growing logic, that logic
-- belongs in the binary (ARCHITECTURE §8).

local rpc = require("froe.rpc")
local ui = require("froe.ui")

local M = {}

M.config = {
  cmd = "froe",
  -- Empty picks by role. :FroeModel trades capability for speed — a 1.5B is
  -- ~9x faster than the 27B and enough for many questions.
  model = "",
  -- "ask" prompts for every mutating call, "accept-edits" auto-approves edits
  -- but still asks before shell commands, "yolo" approves everything the hard
  -- denylist has not already refused.
  mode = "ask",
  show_reasoning = false,
  keys = true,
  -- Command that brings the model runtime up, as a list e.g.
  -- {"lms", "load", "prism-ml/bonsai-27b", "--gpu", "max",
  --  "--context-length", "8192", "--parallel", "1", "--ttl", "1800", "-y"}.
  -- Machine-specific (model key, VRAM budget), so it belongs in your own
  -- config, not a default here. Empty disables :FroeLaunch.
  launch = {},
}

local client = nil
local busy = false

local function notify(msg, level)
  vim.notify("froe: " .. msg, level or vim.log.levels.INFO)
end

--- Ask the user to approve a mutating tool call. The Go side is blocked here,
--- so this must always answer.
local function approve(params)
  local prompt = params.summary or params.tool or "run a tool"
  if params.detail and params.detail ~= "" then
    ui.line("")
    ui.line("  " .. prompt)
    for _, l in ipairs(vim.split(params.detail, "\n", { plain = true })) do
      ui.line("  │ " .. l)
    end
  end
  local choice = vim.fn.confirm("froe wants to " .. prompt, "&Allow\n&Deny\nA&lways", 2)
  if choice == 1 then return "allow" end
  if choice == 3 then return "always" end
  return "deny"
end

local function on_event(p)
  if p.kind == "turn" then
    ui.spinner_stop()
    ui.line("")
    ui.line("── turn " .. (p.turn or "?"))
    -- Still waiting on the model: keep showing that time is passing.
    ui.spinner_start()
  elseif p.kind == "text" then
    ui.spinner_stop()
    ui.stream(p.text or "")
  elseif p.kind == "reasoning" then
    if M.config.show_reasoning then
      ui.stream(p.text or "")
    else
      ui.spinner_start()
    end
  elseif p.kind == "tool" then
    ui.spinner_stop()
    ui.line("  ▸ " .. (p.tool or "") .. " " .. (p.args or ""))
  elseif p.kind == "tool_result" then
    local first = vim.split(p.result or "", "\n", { plain = true })[1] or ""
    ui.line("    " .. first:sub(1, 120))
  elseif p.kind == "denied" then
    ui.line("    declined")
  elseif p.kind == "error" then
    ui.line("  error: " .. (p.text or ""))
  end
end

--- Start the binary and handshake, if not already running.
local function ensure_client(cb)
  if client and client:running() then return cb(true) end

  local c, err = rpc.start(M.config.cmd, on_event, approve)
  if not c then
    notify(err or "could not start", vim.log.levels.ERROR)
    return cb(false)
  end
  client = c
  client:request("initialize", { root = vim.fn.getcwd(), model = M.config.model }, function(result, rerr)
    if rerr then
      notify("initialize failed: " .. (rerr.message or "?"), vim.log.levels.ERROR)
      return cb(false)
    end
    ui.line(("→ %s via %s (%s tools, %s strategy)"):format(
      result.model, result.runtime, #result.tools, result.strategy))
    cb(true)
  end)
end

--- Run a task. selection is optional {text, file, start_line, end_line}.
function M.run(task, selection)
  if busy then
    return notify("already running — :FroeStop to cancel", vim.log.levels.WARN)
  end
  ensure_client(function(ok)
    if not ok then return end
    ui.open()
    ui.line("")
    ui.line("❯ " .. task)

    local params = { task = task, mode = M.config.mode }
    if selection then
      params.file = selection.file
      params.selection = selection.text
      params.start_line = selection.start_line
      params.end_line = selection.end_line
    end

    -- Start the indicator immediately, not on the first reasoning event.
    -- The dead time is BEFORE any event arrives: model load and prefill emit
    -- nothing, so a spinner keyed on reasoning leaves the split looking frozen
    -- for exactly the stretch the user most needs to see is alive. Measured:
    -- 58s for a greeting, of which the first ~50 produced no output at all.
    ui.spinner_start()

    busy = true
    client:request("run", params, function(result, err)
      busy = false
      ui.spinner_stop()
      ui.flush()
      if err then
        if err.code == -32800 then
          ui.line("  cancelled")
        else
          ui.line("  error: " .. (err.message or "?"))
        end
        return
      end
      ui.line(("  %d turns · %d tool calls · %d tokens · %.1fs"):format(
        result.turns or 0, result.tool_calls or 0, result.tokens or 0,
        (result.elapsed_ms or 0) / 1000))
      -- Files may have changed underneath open buffers.
      vim.cmd("checktime")
    end)
  end)
end

--- Read the last visual selection.
local function visual_selection()
  local sl = vim.fn.line("'<")
  local el = vim.fn.line("'>")
  if sl == 0 or el == 0 then return nil end
  local lines = vim.api.nvim_buf_get_lines(0, sl - 1, el, false)
  if #lines == 0 then return nil end
  return {
    text = table.concat(lines, "\n"),
    file = vim.fn.expand("%:."),
    start_line = sl,
    end_line = el,
  }
end

--- Bring the model runtime up via config.launch, e.g. `lms load ...` for a
--- local LM Studio model. Independent of the froe process/RPC handshake -
--- this just gets the backend ready before :Froe tries to talk to it.
function M.launch()
  if #M.config.launch == 0 then
    return notify("no launch command configured - set opts.launch in setup()", vim.log.levels.WARN)
  end

  ui.open()
  ui.line("")
  ui.line("❯ " .. table.concat(M.config.launch, " "))
  ui.spinner_start()

  local lines = {}
  vim.fn.jobstart(M.config.launch, {
    stdout_buffered = true,
    stderr_buffered = true,
    on_stdout = function(_, data) vim.list_extend(lines, data or {}) end,
    on_stderr = function(_, data) vim.list_extend(lines, data or {}) end,
    on_exit = function(_, code)
      ui.spinner_stop()
      -- A progress bar updates one line in place with "\r", never "\n", so
      -- the whole thing can arrive as a single jobstart "line" - only the
      -- text after the last \r is the actual final state worth keeping.
      for _, raw in ipairs(lines) do
        local tail = raw:match("([^\r]*)$")
        if tail and tail:match("%S") then
          ui.line("  " .. tail)
        end
      end
      if code == 0 then
        notify("launch complete")
      else
        notify("launch failed (exit " .. code .. ")", vim.log.levels.ERROR)
      end
    end,
  })
end

function M.stop()
  if not (client and client:running()) then return end
  client:request("cancel", {}, function() end)
  notify("cancelling")
end

function M.restart()
  if client then client:stop() end
  client = nil
  busy = false
  notify("restarted")
end

function M.setup(opts)
  M.config = vim.tbl_deep_extend("force", M.config, opts or {})

  vim.api.nvim_create_user_command("Froe", function(a)
    if a.args == "" then return notify("usage: :Froe <task>") end
    M.run(a.args)
  end, { nargs = "*", desc = "Run a froe task" })

  vim.api.nvim_create_user_command("FroeVisual", function(a)
    local sel = visual_selection()
    if not sel then return notify("no visual selection") end
    local task = a.args ~= "" and a.args or vim.fn.input("froe: ")
    if task == "" then return end
    M.run(task, sel)
  end, { nargs = "*", range = true, desc = "Run a froe task on the selection" })

  vim.api.nvim_create_user_command("FroeModel", function(a)
    if a.args == "" then
      notify("model: " .. (M.config.model ~= "" and M.config.model or "auto"))
      return
    end
    M.config.model = a.args
    M.restart()
    notify("model set to " .. a.args)
  end, { nargs = "?", desc = "Show or change the model (restarts the backend)" })

  vim.api.nvim_create_user_command("FroeLaunch", M.launch, { desc = "Bring the model runtime up (config.launch)" })
  vim.api.nvim_create_user_command("FroeStop", M.stop, { desc = "Cancel the running task" })
  vim.api.nvim_create_user_command("FroeRestart", M.restart, { desc = "Restart the froe process" })
  vim.api.nvim_create_user_command("FroeOpen", ui.open, { desc = "Open the froe output" })
  vim.api.nvim_create_user_command("FroeClose", ui.close, { desc = "Close the froe output" })
  vim.api.nvim_create_user_command("FroeClear", ui.clear, { desc = "Clear the froe output" })

  if M.config.keys then
    vim.keymap.set("n", "<leader>dd", ":Froe ", { desc = "froe: task" })
    vim.keymap.set("v", "<leader>dd", ":FroeVisual ", { desc = "froe: task on selection" })
    vim.keymap.set("n", "<leader>dl", M.launch, { desc = "froe: launch model runtime" })
    vim.keymap.set("n", "<leader>ds", M.stop, { desc = "froe: stop" })
    vim.keymap.set("n", "<leader>do", ui.open, { desc = "froe: open output" })
  end
end

return M
