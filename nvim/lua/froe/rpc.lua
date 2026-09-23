-- Transport for the froe binary: newline-delimited JSON-RPC over stdio.
--
-- This file knows nothing about agents, models or tools. It moves messages.
-- Anything resembling a decision belongs in the Go binary (ARCHITECTURE §8).

local M = {}

local Client = {}
Client.__index = Client

--- Start the froe RPC server.
--- @param cmd string  path to the froe binary
--- @param on_event fun(params: table)          progress notifications
--- @param on_approve fun(params: table): string  returns "allow"|"deny"|"always"
function M.start(cmd, on_event, on_approve)
  local self = setmetatable({
    next_id = 0,
    pending = {},   -- id -> callback awaiting a reply
    buffer = "",    -- partial line carried between stdout chunks
    on_event = on_event,
    on_approve = on_approve,
  }, Client)

  self.job = vim.fn.jobstart({ cmd, "rpc" }, {
    on_stdout = function(_, data) self:_on_stdout(data) end,
    on_stderr = function(_, data)
      -- stderr is diagnostics only; stdout carries the protocol.
      local msg = table.concat(data or {}, "\n")
      if msg:match("%S") then
        vim.schedule(function() vim.notify("froe: " .. msg, vim.log.levels.WARN) end)
      end
    end,
    on_exit = function(_, code)
      self.job = nil
      -- 0 is a clean exit; 143 is SIGTERM, which is what jobstop sends when
      -- Neovim quits or the user restarts the client. Neither is a failure.
      if code ~= 0 and code ~= 143 then
        vim.schedule(function()
          vim.notify("froe exited with code " .. code, vim.log.levels.ERROR)
        end)
      end
    end,
  })

  if self.job <= 0 then
    return nil, "could not start " .. cmd
  end
  return self
end

--- jobstart splits on newlines but does not guarantee complete lines, so a
--- partial message is carried to the next chunk.
function Client:_on_stdout(data)
  if not data then return end
  for i, chunk in ipairs(data) do
    if i > 1 then
      self:_consume(self.buffer)
      self.buffer = ""
    end
    self.buffer = self.buffer .. chunk
  end
end

function Client:_consume(line)
  if not line or not line:match("%S") then return end
  local ok, msg = pcall(vim.json.decode, line)
  if not ok or type(msg) ~= "table" then return end

  -- A reply to something we sent.
  if msg.id and not msg.method then
    local cb = self.pending[msg.id]
    self.pending[msg.id] = nil
    if cb then vim.schedule(function() cb(msg.result, msg.error) end) end
    return
  end

  -- A request from the server: it is blocked waiting for us.
  if msg.id and msg.method == "froe/approve" then
    vim.schedule(function()
      local decision = self.on_approve(msg.params or {})
      self:_send({ jsonrpc = "2.0", id = msg.id, result = { decision = decision } })
    end)
    return
  end

  if msg.method == "froe/event" then
    vim.schedule(function() self.on_event(msg.params or {}) end)
  end
end

function Client:_send(msg)
  if not self.job then return end
  vim.fn.chansend(self.job, vim.json.encode(msg) .. "\n")
end

--- Call a method. cb receives (result, err).
function Client:request(method, params, cb)
  self.next_id = self.next_id + 1
  local id = self.next_id
  if cb then self.pending[id] = cb end
  self:_send({ jsonrpc = "2.0", id = id, method = method, params = params or vim.empty_dict() })
  return id
end

function Client:stop()
  if self.job then
    vim.fn.jobstop(self.job)
    self.job = nil
  end
end

function Client:running() return self.job ~= nil end

return M
