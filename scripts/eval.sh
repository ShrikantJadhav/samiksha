#!/bin/sh
# Convenience wrapper: scripts/eval.sh [eval.py args...]
set -eu
ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
if [ ! -x "$ROOT/samiksha" ]; then
  echo "samiksha binary not found; run 'make build' first" >&2
  exit 2
fi
exec python3 "$ROOT/scripts/eval.py" "$@"
