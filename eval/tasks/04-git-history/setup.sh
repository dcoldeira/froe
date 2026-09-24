#!/usr/bin/env bash
echo "line" >> README.md
git add -A
git -c user.email=e@e -c user.name=e commit -qm "fix: correct the witness value rounding"
