#!/usr/bin/env python3
"""
split-agent.py — split internal/agent/agent.go into focused files.

T05 sibling to split-handler.py. Same pattern: read from git HEAD,
carve named function ranges, rewrite agent.go with the carved blocks
removed.

Usage:
  python scripts/split-agent.py --dry-run
  python scripts/split-agent.py
"""
import argparse
import subprocess
import sys
from pathlib import Path

AGENT = Path('internal/agent/agent.go')

# Line numbers are 1-indexed, inclusive (from
# `git show HEAD:internal/agent/agent.go | grep -n '^func '`).
RANGES = {
    'prompt.go': {
        'header': '''package agent

// prompt.go — system-prompt construction. Every helper here is
// pure: it takes an (optional) agent state slice and a set of
// available tools, and returns the corresponding prompt block as
// a string. Used by buildStaticSystemPrompt to assemble the
// "static" half of the system prompt (the part that benefits from
// LLM-side prompt caching).
//
// Split from agent.go in T05. Behaviour unchanged.

import (
\t"context"
\t"fmt"
\t"log"
\t"runtime"
\t"strings"
\t"time"

\t"github.com/p-chat/pchat/internal/agents"
\t"github.com/p-chat/pchat/internal/config"
\t"github.com/p-chat/pchat/internal/knowledge"
\t"github.com/p-chat/pchat/internal/llm"
\t"github.com/p-chat/pchat/internal/rules"
\t"github.com/p-chat/pchat/internal/skill"
\t"github.com/p-chat/pchat/internal/style"
\t"github.com/p-chat/pchat/internal/tool"
)
''',
        'ranges': [
            (598, 1038),  # buildStaticSystemPrompt + 10 build*Block helpers + buildKBIndex
        ],
    },
    'auto_continue.go': {
        'header': '''package agent

// auto_continue.go — P0-3 auto-continue guard. When the agent
// loop exits because the LLM emitted zero tool calls but the
// session still has pending todos, the guard re-injects a
// user-style "未完成" prompt and re-enters the loop, up to 3
// times per turn. See docs/auto-continue.md for the user-facing
// design.
//
// Split from agent.go in T05. Behaviour unchanged.

import (
\t"fmt"
\t"strings"

\t"github.com/p-chat/pchat/internal/tool"
)
''',
        'ranges': [
            (2934, 3033),  # pickMaxStepsPrompt + sessionPendingTodos + buildAutoContinuePrompt
        ],
    },
    'normalize.go': {
        'header': '''package agent

// normalize.go — message/tool-call normalization. Three concerns:
//
//   1. Guard-reset bookkeeping (resetGuardCounters, resetSameToolErr)
//      so the "same tool errored twice in a row" detector doesn't
//      leak state across rounds.
//   2. tool_call_id assignment + result-pairing for the Anthropic
//      protocol which, unlike OpenAI, requires every tool result
//      to carry the matching tool_use_id (normalizeToolCallIDs,
//      needsNormalizedToolResults, normalizeToolResults).
//
// Split from agent.go in T05. Behaviour unchanged.

import (
\t"github.com/google/uuid"

\t"github.com/p-chat/pchat/internal/llm"
)
''',
        'ranges': [
            (2555, 2654),  # resetGuardCounters + resetSameToolErr + normalize* (3 funcs)
        ],
    },
    'util.go': {
        'header': '''package agent

// util.go — stateless helpers used by ChatWithTools. Categories:
//
//   * Time / size formatting (formatElapsed, truncateToFit)
//   * Tool-result cleanup (truncateToolResult, redactPhantomErrors)
//   * LLM-side markdown-tool-call parsing (parseMarkdownToolCalls,
//     cleanMarkdownToolCalls) — some proxy LLMs emit tool calls
//     as ```json fenced blocks instead of native tool_calls
//   * Retry policy (isRetryable)
//   * Tool-result history pruning (pruneOldToolResults)
//
// Split from agent.go in T05. Behaviour unchanged.

import (
\t"encoding/json"
\t"fmt"
\t"regexp"
\t"strings"
\t"time"

\t"github.com/google/uuid"

\t"github.com/p-chat/pchat/internal/llm"
)
''',
        'ranges': [
            (2539, 2554),  # formatElapsed
            (3034, 3086),  # truncateToolResult
            (3087, 3207),  # parseMarkdownToolCalls + cleanMarkdownToolCalls
            (3208, 3299),  # redactPhantomErrors + isRetryable + pruneOldToolResults + truncateToFit
        ],
    },
}


def read_original_agent() -> list[str]:
    out = subprocess.check_output(
        ['git', 'show', 'HEAD:internal/agent/agent.go'],
        text=True,
        encoding='utf-8',
    )
    return out.splitlines(keepends=True)


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument('--dry-run', action='store_true')
    args = ap.parse_args()

    lines = read_original_agent()
    trailing = ''
    if lines and not lines[-1].endswith('\n'):
        trailing = lines.pop()

    print(f'[split] HEAD agent.go has {len(lines)} lines')

    removed = set()
    for name, r in RANGES.items():
        for s, e in r['ranges']:
            for ln in range(s - 1, e):
                if 0 <= ln < len(lines):
                    removed.add(ln)

    for name, r in RANGES.items():
        chunks = []
        for s, e in r['ranges']:
            chunks.append(''.join(lines[s - 1:e]))
        body = '\n'.join(chunks)
        content = r['header'] + '\n' + body
        if not content.endswith('\n'):
            content += '\n'

        out = Path('internal/agent') / name
        if args.dry_run:
            print(f'[dry-run] would write {out} ({len(content.splitlines())} lines)')
        else:
            out.write_text(content, encoding='utf-8')
            print(f'[split] wrote {out} ({len(content.splitlines())} lines)')

    new_lines = [ln for i, ln in enumerate(lines) if i not in removed]
    if not args.dry_run:
        AGENT.write_text(''.join(new_lines) + trailing, encoding='utf-8')
    print(f'[split] agent.go: {len(lines)} -> {len(new_lines)} lines')

    return 0


if __name__ == '__main__':
    sys.exit(main())
