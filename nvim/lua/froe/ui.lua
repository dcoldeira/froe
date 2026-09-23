-- The output window. Rendering only.

local M = {}

local state = { buf = nil, win = nil, pending_line = "" }

local function ensure_buf()
  if state.buf and vim.api.nvim_buf_is_valid(state.buf) then return state.buf end
  state.buf = vim.api.nvim_create_buf(false, true)
  vim.bo[state.buf].buftype = "nofile"
  vim.bo[state.buf].bufhidden = "hide"
  vim.bo[state.buf].swapfile = false
  vim.bo[state.buf].filetype = "markdown"
  vim.api.nvim_buf_set_name(state.buf, "froe")
  return state.buf
end

--- Open the output split, reusing it if already visible.
function M.open()
  local buf = ensure_buf()
  if state.win and vim.api.nvim_win_is_valid(state.win) then
    return state.win
  end
  local current = vim.api.nvim_get_current_win()
  vim.cmd("botright vsplit")
  state.win = vim.api.nvim_get_current_win()
  vim.api.nvim_win_set_buf(state.win, buf)
  vim.wo[state.win].wrap = true
  vim.wo[state.win].number = false
  vim.wo[state.win].relativenumber = false
  vim.wo[state.win].signcolumn = "no"
  vim.api.nvim_set_current_win(current)
  return state.win
end

function M.close()
  if state.win and vim.api.nvim_win_is_valid(state.win) then
    vim.api.nvim_win_close(state.win, true)
  end
  state.win = nil
end

--- Append whole lines. Streamed text arrives in fragments, so a partial line is
--- held until its newline rather than creating one buffer line per token.
function M.stream(text)
  local buf = ensure_buf()
  state.pending_line = state.pending_line .. text
  if not state.pending_line:find("\n") then
    M._replace_last(state.pending_line)
    return
  end
  local parts = vim.split(state.pending_line, "\n", { plain = true })
  state.pending_line = table.remove(parts)
  M._replace_last(parts[1] or "")
  for i = 2, #parts do
    M._append(parts[i])
  end
  M._append(state.pending_line)
end

function M.line(text)
  M.flush()
  for _, l in ipairs(vim.split(text or "", "\n", { plain = true })) do
    M._append(l)
  end
end

--- Commit any partial line so the next write starts cleanly.
function M.flush()
  if state.pending_line ~= "" then
    state.pending_line = ""
    M._append("")
  end
end

function M._append(line)
  local buf = ensure_buf()
  vim.bo[buf].modifiable = true
  vim.api.nvim_buf_set_lines(buf, -1, -1, false, { line })
  vim.bo[buf].modifiable = false
  M._follow()
end

function M._replace_last(line)
  local buf = ensure_buf()
  local count = vim.api.nvim_buf_line_count(buf)
  vim.bo[buf].modifiable = true
  vim.api.nvim_buf_set_lines(buf, count - 1, count, false, { line })
  vim.bo[buf].modifiable = false
  M._follow()
end

--- Keep the newest output visible, but only when the cursor is already at the
--- bottom — scrolling away to read something must not be yanked back.
function M._follow()
  if not (state.win and vim.api.nvim_win_is_valid(state.win)) then return end
  local buf = ensure_buf()
  local count = vim.api.nvim_buf_line_count(buf)
  local cur = vim.api.nvim_win_get_cursor(state.win)[1]
  if cur >= count - 2 then
    vim.api.nvim_win_set_cursor(state.win, { count, 0 })
  end
end

-- Words shown while the model reasons.
--
-- On a ~5 tok/s local model the reasoning phase runs for a minute or more and
-- produces no visible output, so without this the split simply looks frozen.
-- The word is decoration; the ELAPSED SECONDS are the real signal that
-- something is still happening.
local WORDS = {
  "Ruminating", "Percolating", "Cogitating", "Marinating", "Pondering",
  "Deliberating", "Mulling", "Chewing", "Noodling", "Simmering",
  "Untangling", "Puzzling", "Brewing", "Scheming", "Contemplating",
}

-- After this long with no output, say what is probably happening. A local
-- model that has been unloaded takes tens of seconds to come back, and
-- "still working" is far less alarming than a counter climbing in silence.
local SLOW_HINT_SECS = 20

local spinner = { timer = nil, started = 0, word = nil, active = false }
local FRAMES = { "⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏" }

--- Begin the thinking indicator. Safe to call repeatedly.
function M.spinner_start()
  if spinner.active then return end
  spinner.active = true
  spinner.started = vim.loop.now()
  spinner.word = WORDS[math.random(#WORDS)]
  M.flush()
  M._append("")

  spinner.timer = vim.loop.new_timer()
  local frame = 0
  spinner.timer:start(0, 120, vim.schedule_wrap(function()
    if not spinner.active then return end
    frame = frame + 1
    local secs = (vim.loop.now() - spinner.started) / 1000
    local hint = ""
    if secs > SLOW_HINT_SECS then
      hint = "  (loading the model or processing a large prompt)"
    end
    M._replace_last(("  %s %s… %.0fs%s"):format(
      FRAMES[(frame % #FRAMES) + 1], spinner.word, secs, hint))
  end))
end

--- Stop the indicator and remove its line.
function M.spinner_stop()
  if not spinner.active then return end
  spinner.active = false
  if spinner.timer then
    spinner.timer:stop()
    spinner.timer:close()
    spinner.timer = nil
  end
  M._replace_last("")
end

function M.spinner_active() return spinner.active end

function M.clear()
  local buf = ensure_buf()
  vim.bo[buf].modifiable = true
  vim.api.nvim_buf_set_lines(buf, 0, -1, false, {})
  vim.bo[buf].modifiable = false
  state.pending_line = ""
end

return M
