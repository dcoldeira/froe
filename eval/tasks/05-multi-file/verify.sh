#!/usr/bin/env bash
export PATH="$PATH:$HOME/.local/go/bin"
go build ./... 2>&1 || exit 1
grep -q "func Get(" internal/store/store.go || exit 1
grep -q "store.Get(" main.go || exit 1
grep -q "Fetch" internal/store/store.go main.go && exit 1
exit 0
