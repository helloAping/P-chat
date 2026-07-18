#!/usr/bin/env python3
"""
split-handler.py — split internal/server/handler.go into focused files.

Idempotent: always reads from git HEAD, so reruns are safe even if
handler.go has been partially modified by a previous run.

Usage:
  python scripts/split-handler.py --dry-run
  python scripts/split-handler.py
"""
import argparse
import subprocess
import sys
from pathlib import Path

HANDLER = Path('internal/server/handler.go')

# 'ranges' is a list of (start_line, end_line) tuples in 1-indexed,
# inclusive form (as reported by `git show HEAD:handler.go | grep -n '^func '`).
# `end_line` is the LAST line of the last function in the range. Multiple
# tuples for the same file are concatenated in order with a blank line.
# Numbers are based on the original 3819-line handler.go from git HEAD.
RANGES = {
    'styles.go': {
        'header': '''package server

// styles.go — style CRUD endpoints.
//
//   GET    /api/v1/styles
//   POST   /api/v1/styles
//   GET    /api/v1/styles/:id
//   PATCH  /api/v1/styles/:id
//   DELETE /api/v1/styles/:id
//
// Split from handler.go in T04. Behaviour unchanged.

import (
\t"net/http"
\t"strings"

\t"github.com/gin-gonic/gin"
\t"github.com/p-chat/pchat/internal/style"
)
''',
        'ranges': [(834, 988)],
    },
    'mcp.go': {
        'header': '''package server

// mcp.go — Model Context Protocol server CRUD endpoints.
//
//   GET    /api/v1/mcp/servers
//   POST   /api/v1/mcp/servers
//   DELETE /api/v1/mcp/servers/:name
//   POST   /api/v1/mcp/servers/:name/restart
//   PATCH  /api/v1/mcp/servers/:name/global
//
// Split from handler.go in T04. Behaviour unchanged.

import (
\t"fmt"
\t"log"
\t"net/http"
\t"os"
\t"path/filepath"
\t"strings"
\t"time"

\t"github.com/gin-gonic/gin"
\t"github.com/p-chat/pchat/internal/config"
\t"github.com/p-chat/pchat/internal/mcp"
)
''',
        'ranges': [(3630, 3819)],
    },
    'projects.go': {
        'header': '''package server

// projects.go — registered project directory CRUD.
//
//   GET    /api/v1/projects
//   POST   /api/v1/projects
//   DELETE /api/v1/projects/:path
//
// Split from handler.go in T04. Behaviour unchanged.

import (
\t"net/http"

\t"github.com/gin-gonic/gin"
\t"github.com/p-chat/pchat/internal/project"
)
''',
        'ranges': [(3443, 3512)],
    },
    'sessions.go': {
        'header': '''package server

// sessions.go — session CRUD + archive + meta + rollback.
//
//   POST   /api/v1/sessions                       (CreateSession)
//   GET    /api/v1/sessions                       (ListSessions)
//   GET    /api/v1/sessions/archived             (ListArchived)
//   GET    /api/v1/sessions/:id                   (GetSession)
//   DELETE /api/v1/sessions/:id                   (DeleteSession)
//   DELETE /api/v1/sessions/:id/permanent         (PermanentDeleteSession)
//   POST   /api/v1/sessions/:id/clear             (ClearSessionMessages)
//   POST   /api/v1/sessions/:id/fork              (ForkSession)
//   POST   /api/v1/sessions/:id/rollback          (RollbackMessages)
//   POST   /api/v1/sessions/:id/undo-rollback     (UndoRollback)
//   PATCH  /api/v1/sessions/:id                   (RenameSession)
//   PUT    /api/v1/sessions/:id/meta              (UpdateSessionMeta)
//   POST   /api/v1/sessions/:id/archive           (ArchiveSession)
//   POST   /api/v1/sessions/:id/unarchive         (UnarchiveSession)
//
// Split from handler.go in T04. Behaviour unchanged.

import (
\t"database/sql"
\t"encoding/json"
\t"fmt"
\t"io"
\t"net/http"
\t"os"
\t"strconv"
\t"strings"
\t"time"

\t"github.com/gin-gonic/gin"
\t"github.com/p-chat/pchat/internal/llm"
\t"github.com/p-chat/pchat/internal/memory"
)
''',
        'ranges': [
            (1025, 1718),   # ListSessions through deref (sessions / rollback / meta)
            (3579, 3629),   # Archive / Unarchive / ListArchived
        ],
    },
    'message_helpers.go': {
        'header': '''package server

// message_helpers.go — read-only message endpoints + render helpers.
//
// Endpoints:
//   GET  /api/v1/sessions/:id/messages          (ListMessages)
//
// Plus the cross-cutting helpers that turn stored llm.ChatMessage
// rows into the wire MessageResponse shape, the snapshot/context
// inspector endpoints, and the SSE frame-line parsing helpers used
// by the message endpoints.
//
// Split from handler.go in T04. Behaviour unchanged.

import (
\t"encoding/json"
\t"fmt"
\t"net/http"
\t"strconv"
\t"strings"
\t"time"

\t"github.com/gin-gonic/gin"
\t"github.com/p-chat/pchat/internal/llm"
\t"github.com/p-chat/pchat/internal/memory"
)
''',
        'ranges': [
            (1719, 1895),   # ListMessages
            (1896, 2002),   # SnapshotRecovery
            (2003, 2128),   # ContextInspector
            (2129, 2159),   # mergeConsecutiveAssistant
            (2160, 2202),   # mergeAssistantRun
            (2203, 2284),   # buildMessageResponse
            (2285, 2298),   # parseInt64Query
            (2299, 2314),   # parseIntQuery
            (2315, 2323),   # isMediaType
            (2324, 2339),   # defaultMIMEForType
            (2340, 2355),   # typeURLFor
            (2356, 2370),   # kindFor
            (2371, 2385),   # mimeFromDataURL
            (2386, 2425),   # inferTextPartMeta
            (2426, 2463),   # buildLLMMessages
            (2464, 2513),   # decodePartsFromMeta
            (2514, 2531),   # hasTextOrThinking
            (3178, 3213),   # sessionToResponse
            (3214, 3226),   # ParseLimit
        ],
    },
    'messages.go': {
        'header': '''package server

// messages.go — the user-facing message endpoint that drives the
// chat loop:
//
//   POST /api/v1/sessions/:id/messages            (SendMessage)
//
// The actual SSE frame emission lives in respondSSE; the chunk
// → wire mapping lives in stream_adapter.go (T04 sibling).
// loadUserMessageSummary is the regen-reply input helper.
//
// Split from handler.go in T04. Behaviour unchanged.

import (
\t"database/sql"
\t"encoding/json"
\t"fmt"
\t"io"
\t"log"
\t"net/http"
\t"strings"
\t"time"

\t"github.com/gin-gonic/gin"
\t"github.com/p-chat/pchat/internal/agent"
\t"github.com/p-chat/pchat/internal/llm"
\t"github.com/p-chat/pchat/internal/style"
\t"github.com/p-chat/pchat/internal/trace"
)
''',
        'ranges': [
            (2532, 2685),   # SendMessage
            (2686, 2788),   # respondSSE
            (3037, 3099),   # loadUserMessageSummary
        ],
    },
    'regen.go': {
        'header': '''package server

// regen.go — regenerate-reply endpoints (P1-4 regen history).
//
//   POST /api/v1/sessions/:id/regenerate                       (Regenerate)
//   GET  /api/v1/sessions/:id/regen-replies                   (ListRegenReplies)
//   POST /api/v1/sessions/:id/regen-replies/:mid/activate      (ActivateRegenReply)
//
// Split from handler.go in T04. Behaviour unchanged.

import (
\t"encoding/json"
\t"fmt"
\t"net/http"
\t"strconv"
\t"strings"
\t"time"

\t"github.com/gin-gonic/gin"
\t"github.com/p-chat/pchat/internal/agent"
\t"github.com/p-chat/pchat/internal/llm"
\t"github.com/p-chat/pchat/internal/style"
\t"github.com/p-chat/pchat/internal/trace"
)
''',
        'ranges': [
            (2789, 2969),   # Regenerate
            (2970, 3036),   # ListRegenReplies
            (3100, 3177),   # ActivateRegenReply
        ],
    },
    'interactive.go': {
        'header': '''package server

// interactive.go — interactive mid-loop endpoints that the agent
// pings during a chat turn (question, confirm, plan) and the
// auxiliary GET/POST endpoints for todos, system messages, and
// summarizer configuration.
//
//   POST /api/v1/sessions/:id/question            (QuestionResponse)
//   POST /api/v1/sessions/:id/confirm             (ConfirmResponse)
//   POST /api/v1/sessions/:id/execute-plan        (ExecutePlan)
//   POST /api/v1/sessions/:id/system-message      (SaveSystemMessage)
//   GET  /api/v1/sessions/:id/todos               (GetTodos)
//   POST /api/v1/sessions/:id/compress            (CompressConversation)
//   POST /api/v1/sessions/:id/reasoning-effort    (SetReasoningEffort)
//
// Split from handler.go in T04. Behaviour unchanged.

import (
\t"encoding/json"
\t"fmt"
\t"net/http"
\t"strconv"
\t"strings"
\t"time"

\t"github.com/gin-gonic/gin"
\t"github.com/p-chat/pchat/internal/llm"
\t"github.com/p-chat/pchat/internal/memory"
\t"github.com/p-chat/pchat/internal/tool"
)
''',
        'ranges': [
            (3246, 3269),   # CompressConversation
            (3270, 3297),   # SetReasoningEffort
            (3298, 3329),   # SaveSystemMessage
            (3330, 3355),   # GetTodos
            (3356, 3372),   # QuestionResponse
            (3373, 3393),   # ConfirmResponse
            (3394, 3442),   # ExecutePlan
        ],
    },
}


def read_original_handler() -> list[str]:
    """Always read from git HEAD so re-runs are idempotent."""
    out = subprocess.check_output(
        ['git', 'show', 'HEAD:internal/server/handler.go'],
        text=True,
        encoding='utf-8',
    )
    return out.splitlines(keepends=True)


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument('--dry-run', action='store_true')
    args = ap.parse_args()

    lines = read_original_handler()
    trailing = ''
    if lines and not lines[-1].endswith('\n'):
        trailing = lines.pop()

    print(f'[split] HEAD handler.go has {len(lines)} lines')

    # Build the "removed" set across all files.
    removed = set()
    for name, r in RANGES.items():
        for s, e in r['ranges']:
            for ln in range(s - 1, e):
                if 0 <= ln < len(lines):
                    removed.add(ln)

    # Write each carved file.
    for name, r in RANGES.items():
        chunks = []
        for s, e in r['ranges']:
            chunks.append(''.join(lines[s - 1:e]))
        body = '\n'.join(chunks)
        content = r['header'] + '\n' + body
        if not content.endswith('\n'):
            content += '\n'

        out = Path('internal/server') / name
        if args.dry_run:
            print(f'[dry-run] would write {out} ({len(content.splitlines())} lines)')
        else:
            out.write_text(content, encoding='utf-8')
            print(f'[split] wrote {out} ({len(content.splitlines())} lines)')

    # Rewrite handler.go with carved lines removed.
    new_lines = [ln for i, ln in enumerate(lines) if i not in removed]
    new_handler = ''.join(new_lines) + trailing

    # Strip trailing blank-line runs in handler.go to keep it tidy.
    if not args.dry_run:
        HANDLER.write_text(new_handler, encoding='utf-8')
    print(f'[split] handler.go: {len(lines)} -> {len(new_lines)} lines')

    return 0


if __name__ == '__main__':
    sys.exit(main())
