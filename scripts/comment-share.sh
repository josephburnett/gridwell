#!/bin/sh
# comment-share: comment bytes as a share of source bytes, over the
# production tree: Go and TS, with no tests, generated code or harnesses. A comment line starts with //, /* or *.
#
#   scripts/comment-share.sh            the tree total
#   scripts/comment-share.sh --files    every file, largest comment share first
#   scripts/comment-share.sh <path>...  those files or directories
set -e
cd "$(dirname "$0")/.."
mode=total
if [ "$1" = "--files" ]; then mode=files; shift; fi
if [ $# -gt 0 ]; then
  files=$(find "$@" -type f \( -name '*.go' -o -name '*.ts' \) | grep -vE 'node_modules|/gen/|/dist/|/out/|_test\.go|\.test\.ts|\.spec\.ts|\.d\.ts' | sort)
else
  files=$(find . -path ./.git -prune -o -type f \( -name '*.go' -o -name '*.ts' \) -print \
    | grep -vE 'node_modules|/gen/|/dist/|/out/|_test\.go|\.test\.ts|\.spec\.ts|\.d\.ts|/e2e/|e2e-web|playwright|/harness/|/[a-z]+test/|test/boundary' \
    | sort)
fi
awk -v mode="$mode" '
  FNR == 1 { if (FILENAME != prev && prev != "") flush(prev); prev = FILENAME; c = 0; k = 0 }
  /^[[:space:]]*(\/\/|\/\*|\*)/ { c += length($0) + 1; next }
  { k += length($0) + 1 }
  function flush(f) { C += c; K += k; if (mode == "files") printf "%7d %7d %3d%% %s\n", c, k, 100 * c / (c + k), f }
  END { flush(prev); printf "%s%d comment bytes, %d code bytes, comment share %d%%\n", (mode == "files" ? "TOTAL " : ""), C, K, 100 * C / (C + K) }
' $files | { if [ "$mode" = files ]; then sort -k3 -rn; else cat; fi; }
