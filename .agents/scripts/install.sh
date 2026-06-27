#!/usr/bin/env bash
# Install agent tool symlinks pointing to .agents/AGENTS.md.
#
# Creates:
#   .opencode/AGENTS.md  -> ./.agents/AGENTS.md
#   .codex/AGENTS.md     -> ./.agents/AGENTS.md
#   .claude/CLAUDE.md    -> ./.agents/AGENTS.md
#
# Usage: bash .agents/scripts/install.sh [--force] [--dry-run]
#
# On Windows, the bash that ships with Git for Windows (MSYS) does
# NOT create real Windows symlinks by default — it creates "Git
# symlinks" that PowerShell and the Windows shell cannot follow.
# We force native symlinks by exporting MSYS=winsymlinks:native so
# `ln -s` produces a real Windows reparse point that any process
# (including pchat-gui, opencode, and the PowerShell install
# script) can resolve.

set -euo pipefail
export MSYS=${MSYS:-winsymlinks:native}

SCRIPT_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" && pwd )"
REPO_ROOT="$( cd "$SCRIPT_DIR/../.." && pwd )"
CANONICAL="$REPO_ROOT/.agents/AGENTS.md"

FORCE=0
DRY_RUN=0
for arg in "$@"; do
    case "$arg" in
        --force) FORCE=1 ;;
        --dry-run) DRY_RUN=1 ;;
        *) echo "Unknown arg: $arg" >&2; exit 1 ;;
    esac
done

# Validate the canonical file exists.
if [ ! -f "$CANONICAL" ]; then
    echo "Canonical file not found: $CANONICAL" >&2
    exit 1
fi

# Each tool's expected location -> target relative to the link's
# parent directory.
install_link() {
    local link_abs="$1"
    local rel_target="$2"
    local link_rel="${link_abs#$REPO_ROOT/}"

    if [ "$DRY_RUN" = "1" ]; then
        echo "  [dry-run] $link_rel  ->  $rel_target"
        return
    fi

    mkdir -p "$(dirname "$link_abs")"

    # If the link already exists, decide what to do.
    if [ -L "$link_abs" ]; then
        local existing
        existing=$(readlink "$link_abs")
        if [ "$existing" = "$rel_target" ]; then
            echo "  [ok]      $link_rel (already points to $rel_target)"
            return
        fi
        if [ "$FORCE" = "1" ]; then
            rm -f "$link_abs"
        else
            echo "  [skip]    $link_rel already a symlink (target=$existing, want=$rel_target). Use --force to replace."
            return
        fi
    elif [ -e "$link_abs" ]; then
        if [ "$FORCE" = "1" ]; then
            rm -rf "$link_abs"
        else
            echo "  [skip]    $link_rel is a real file/dir. Use --force to replace."
            return
        fi
    fi

    if ln -s "$rel_target" "$link_abs" 2>/dev/null; then
        echo "  [linked]  $link_rel  ->  $rel_target"
    elif [ -f "$rel_target" ] && command -v cmd.exe >/dev/null 2>&1; then
        # Fallback: use Windows mklink through cmd. This is
        # only needed in very old or restricted environments
        # where `ln -s` (even with MSYS=winsymlinks:native)
        # cannot create a symlink.
        if cmd.exe //c "mklink \"$link_abs\" \"$rel_target\"" >/dev/null 2>&1; then
            echo "  [linked]  $link_rel  ->  $rel_target  (via mklink)"
        else
            # Final fallback: copy the file. Future edits to
            # the canonical won't propagate; the user will
            # need to re-run this script to sync.
            cp "$rel_target" "$link_abs"
            echo "  [copied]  $link_rel  <-  $rel_target  (no symlink: permission denied)"
        fi
    else
        # Non-Windows: no cmd.exe. Just copy.
        cp "$rel_target" "$link_abs"
        echo "  [copied]  $link_rel  <-  $rel_target  (no symlink: ln -s failed)"
    fi
}

echo ""
echo "Installing agent tool symlinks (canonical: $CANONICAL)"
echo "Repo root: $REPO_ROOT"
echo ""

install_link "$REPO_ROOT/.opencode/AGENTS.md" "../.agents/AGENTS.md"
install_link "$REPO_ROOT/.codex/AGENTS.md"    "../.agents/AGENTS.md"
install_link "$REPO_ROOT/.claude/CLAUDE.md"   "../.agents/AGENTS.md"

# If the root AGENTS.md is the project-init stub (template text
# "在此描述你的项目"), replace it with a symlink so all paths
# lead to the same canonical file.
ROOT_AGENTS="$REPO_ROOT/AGENTS.md"
if [ -e "$ROOT_AGENTS" ] && [ ! -L "$ROOT_AGENTS" ]; then
    if grep -q "在此描述你的项目" "$ROOT_AGENTS" 2>/dev/null; then
        rel_root="../.agents/AGENTS.md"
        if [ "$DRY_RUN" = "1" ]; then
            echo "  [dry-run] AGENTS.md (root)  ->  $rel_root  (would replace stub)"
        else
            rm -f "$ROOT_AGENTS"
            if ln -s "$rel_root" "$ROOT_AGENTS" 2>/dev/null; then
                echo "  [linked]  AGENTS.md (root)  ->  $rel_root  (was a stub)"
            elif command -v cmd.exe >/dev/null 2>&1; then
                if cmd.exe //c "mklink \"$ROOT_AGENTS\" \"$rel_root\"" >/dev/null 2>&1; then
                    echo "  [linked]  AGENTS.md (root)  ->  $rel_root  (via mklink)"
                else
                    cp "$rel_root" "$ROOT_AGENTS"
                    echo "  [copied]  AGENTS.md (root)  <-  $rel_root  (no symlink)"
                fi
            else
                cp "$rel_root" "$ROOT_AGENTS"
                echo "  [copied]  AGENTS.md (root)  <-  $rel_root  (no symlink)"
            fi
        fi
    else
        echo "  [skip]    AGENTS.md (root) is non-trivial content; not touching it."
    fi
elif [ -L "$ROOT_AGENTS" ]; then
    echo "  [ok]      AGENTS.md (root) is already a symlink"
fi

echo ""
echo "Done. Agent tools will now read .agents/AGENTS.md."
echo "Edit .agents/AGENTS.md to update the canonical spec for all tools."
