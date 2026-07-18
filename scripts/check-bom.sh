#!/usr/bin/env bash
# =============================================================================
# check-bom.sh — lint: fail if any source file starts with a UTF-8 BOM
# =============================================================================
# Why:  `go test -cover` (and a handful of other Go-toolchain paths) reject
#       files that start with a UTF-8 BOM (EF BB BF). `go build` tolerates
#       them, so a BOM can sneak in via an editor save and only surface in
#       coverage / CI runs. This script catches the issue at lint time.
#
# Usage:
#   ./scripts/check-bom.sh                 # scan tracked + untracked files
#   ./scripts/check-bom.sh --staged        # only files staged for commit
#
# Exit codes:
#   0  no BOMs found
#   1  one or more files start with a BOM (their paths are printed)
#   2  internal error (e.g. `git` not on PATH)
# =============================================================================

set -u

mode="all"
if [ "${1:-}" = "--staged" ]; then
  mode="staged"
fi

# Extensions we care about. `.md` is included because some Windows
# editors add BOMs to Markdown too, which breaks `cat` piping into
# `grep` / `awk` and (less importantly) inflates file size.
exts='go ts tsx js jsx vue css scss html json yml yaml toml sh ps1 bat cmd md'

fail=0
count=0

check_file() {
  local f="$1"
  # First 3 bytes via `head -c 3`. We're explicit about the byte sequence
  # (EF BB BF) rather than calling `file` so the script works on minimal
  # Alpine / busybox environments where `file` may not be installed.
  local sig
  sig=$(head -c 3 -- "$f" 2>/dev/null | od -An -tx1 | tr -d ' \n')
  if [ "$sig" = "efbbbf" ]; then
    printf '  BOM: %s\n' "$f"
    fail=1
    count=$((count + 1))
  fi
}

matches_ext() {
  local f="$1"
  case "$f" in
    *.go|*.ts|*.tsx|*.js|*.jsx|*.vue|*.css|*.scss|*.html|*.json|*.yml|*.yaml|*.toml|*.sh|*.ps1|*.bat|*.cmd|*.md)
      return 0 ;;
    *)
      return 1 ;;
  esac
}

# IMPORTANT: read from the `< <(...)` process-substitution form, NOT
# `cmd | while read`. The pipe spawns a subshell whose variable writes
# never propagate back to the parent; the process-substitution runs
# the loop in the current shell so `fail` / `count` survive.
read_null() {
  while IFS= read -r -d '' f; do
    if matches_ext "$f"; then
      check_file "$f"
    fi
  done
}

# Build the file list. `git ls-files` is the source of truth for what's
# tracked; we also pull `--others --exclude-standard` so untracked files
# are linted too (an untracked BOM file is still a BOM file).
#
# For `--staged`, we use `git diff --cached --name-only` because
# `git ls-files --cached` always returns the full tracked set, not
# the diff against HEAD. `--diff-filter=ACMR` skips deleted entries.
case "$mode" in
  staged)
    while IFS= read -r -d '' f; do
      if matches_ext "$f"; then
        check_file "$f"
      fi
    done < <(git diff --cached --name-only --diff-filter=ACMR -z 2>/dev/null)
    ;;
  *)
    while IFS= read -r -d '' f; do
      if matches_ext "$f"; then
        check_file "$f"
      fi
    done < <(git ls-files -z 2>/dev/null)
    # Also scan untracked files so a fresh `git add foo.go` (BOM
    # included) gets flagged before the commit.
    while IFS= read -r -d '' f; do
      if matches_ext "$f"; then
        check_file "$f"
      fi
    done < <(git ls-files --others --exclude-standard -z 2>/dev/null)
    ;;
esac

if [ "$fail" -ne 0 ]; then
  printf '\n[check-bom] FAIL: %d file(s) start with a UTF-8 BOM.\n' "$count" >&2
  printf '[check-bom] Strip with:  ./scripts/strip-bom.ps1 -Path <file>\n' >&2
  exit 1
fi

printf '[check-bom] OK: no BOMs found.\n'
exit 0
