#!/usr/bin/env bash
export PATH="$PATH:$HOME/.local/go/bin"
go test ./... 2>&1 | grep -q "^ok" || exit 1
