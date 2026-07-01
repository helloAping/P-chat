#!/usr/bin/env bash
# Install agent tool directory symlinks pointing to .agents/.
#
# Creates directory-level symlinks so all tools share the entire
# .agents/ tree (AGENTS.md + docs/ + scripts/):
#
#   .opencode  ->  .agents/    (opencode reads .opencode/AGENTS.md)
#   .codex     ->  .agents/    (codex reads .codex/AGENTS.md)
#   .claude    ->  .agents/    (claude reads .claude/CLAUDE.md)
#
# Usage: bash .agents/scripts/install.sh [--force] [--dry-run]
#
# On Windows (MSYS/Git Bash), native symlinks are forced via
# MSYS=winsymlinks:native so that PowerShell and the Windows
# shell can resolve them.  Directory symlinks on Windows use
# `mklink /J` (Junction) which does not require admin rights.

set -euo pipefail
export MSYS=${MSYS:-winsymlinks:native}

SCRIPT_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" && pwd )"
REPO_ROOT="$( cd "$SCRIPT_DIR/../.." && pwd )"
AGENTS_DIR="$REPO_ROOT/.agents"
CANONICAL="$AGENTS_DIR/AGENTS.md"

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

# Ensure .agents/CLAUDE.md exists for claude.
CLAUDE_FILE="$AGENTS_DIR/CLAUDE.md"
if [ ! -f "$CLAUDE_FILE" ]; then
    cp "$CANONICAL" "$CLAUDE_FILE"
    echo "  Created $CLAUDE_FILE (copy of AGENTS.md for claude)"
fi

# Directory-level symlink: remove old target, create link.
install_dir_link() {
    local link_path="$1"      # absolute: .opencode
    local link_rel="${link_path#$REPO_ROOT/}"
    local target_abs="$AGENTS_DIR"

    if [ "$DRY_RUN" = "1" ]; then
        echo "  [dry-run] $link_rel  ->  .agents/"
        return
    fi

    # Remove existing target if replacing.
    if [ -L "$link_path" ]; then
        local existing
        existing=$(readlink "$link_path")
        if [ "$existing" = "$target_abs" ]; then
            echo "  [ok]      $link_rel (already points to .agents/)"
            return
        fi
        if [ "$FORCE" = "1" ]; then
            rm -rf "$link_path"
        else
            echo "  [skip]    $link_rel already a symlink (target=$existing). Use --force."
            return
        fi
    elif [ -e "$link_path" ]; then
        if [ "$FORCE" = "1" ]; then
            rm -rf "$link_path"
        else
            echo "  [skip]    $link_rel exists. Use --force to replace."
            return
        fi
    fi

    # Try native ln -s first.
    if ln -s "$target_abs" "$link_path" 2>/dev/null; then
        echo "  [linked]  $link_rel  ->  .agents/"
    elif command -v cmd.exe >/dev/null 2>&1; then
        # Windows fallback: mklink /J (Junction, no admin needed).
        if cmd.exe //c "mklink /J \"$link_path\" \"$target_abs\"" >/dev/null 2>&1; then
            echo "  [junction] $link_rel  ->  .agents/"
        elif cmd.exe //c "mklink /D \"$link_path\" \".agents\"" >/dev/null 2>&1; then
            echo "  [symlink]  $link_rel  ->  .agents/"
        else
            # Last resort: copy files.
            mkdir -p "$link_path"
            cp -r "$target_abs"/* "$link_path/" 2>/dev/null || true
            echo "  [copied]   $link_rel  <-  .agents/  (no symlink available)"
        fi
    else
        # Unix but ln -s failed: copy.
        mkdir -p "$link_path"
        cp -r "$target_abs"/* "$link_path/" 2>/dev/null || true
        echo "  [copied]   $link_rel  <-  .agents/  (ln -s failed)"
    fi
}

echo ""
echo "Installing agent tool directory links -> .agents/"
echo "Repo root: $REPO_ROOT"
echo ""

install_dir_link "$REPO_ROOT/.opencode"
install_dir_link "$REPO_ROOT/.codex"
install_dir_link "$REPO_ROOT/.claude"

# .p-chat has local config content; sync AGENTS.md only.
PCHAT_DIR="$REPO_ROOT/.p-chat"
if [ -d "$PCHAT_DIR" ]; then
    if [ "$DRY_RUN" = "1" ]; then
        echo "  [dry-run] .p-chat/AGENTS.md  <-  .agents/AGENTS.md"
    else
        cp "$CANONICAL" "$PCHAT_DIR/AGENTS.md"
        echo "  [copied]  .p-chat/AGENTS.md  <-  .agents/AGENTS.md  (file level, .p-chat has local config)"
    fi
fi

# Root AGENTS.md stub replacement.
ROOT_AGENTS="$REPO_ROOT/AGENTS.md"
if [ -e "$ROOT_AGENTS" ] && [ ! -L "$ROOT_AGENTS" ]; then
    if grep -q "在此描述你的项目" "$ROOT_AGENTS" 2>/dev/null; then
        if [ "$DRY_RUN" = "1" ]; then
            echo "  [dry-run] AGENTS.md (root) would be replaced"
        else
            rm -f "$ROOT_AGENTS"
            if ln -s ".agents/AGENTS.md" "$ROOT_AGENTS" 2>/dev/null; then
                echo "  [linked]  AGENTS.md (root)  ->  .agents/AGENTS.md"
            else
                cp "$CANONICAL" "$ROOT_AGENTS"
                echo "  [copied]  AGENTS.md (root)  <-  .agents/AGENTS.md"
            fi
        fi
    else
        echo "  [skip]    AGENTS.md (root) is non-trivial; not touching it."
    fi
elif [ -L "$ROOT_AGENTS" ]; then
    echo "  [ok]      AGENTS.md (root) is already a symlink"
fi

echo ""
echo "Done. Agent tools will now read .agents/ content via:"
echo "  .opencode/AGENTS.md  -> .agents/AGENTS.md"
echo "  .codex/AGENTS.md     -> .agents/AGENTS.md"
echo "  .claude/CLAUDE.md    -> .agents/CLAUDE.md"
echo "  .opencode/docs/      -> .agents/docs/"
