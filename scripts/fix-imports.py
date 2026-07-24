#!/usr/bin/env python3
"""
fix-imports.py — iteratively remove unused imports.

Loops:  run go build, parse unused-import errors, delete those lines,
re-run. Stops when the build is clean (or only has non-import errors).

This is a one-shot helper for the T04 split: the carved files end up
with more imports than they need because we don't know exactly which
package symbols each function uses until after the split.
"""
import re
import subprocess
import sys
from pathlib import Path

ROOT = Path('internal/server')

# Map: file line like "internal\server\handler.go:5:2" → we just need
# the file basename + the line number to delete the import line.
# The error message always points at the quoted import path, which is
# what we strip.
UNUSED = re.compile(
    r'^(?P<file>[^:]+):(?P<line>\d+):\d+: "(?P<import>[^"]+)" imported and not used$'
)


def run_go_build() -> tuple[int, str]:
    out = subprocess.run(
        ['go', 'build', './...'],
        capture_output=True,
        text=True,
    )
    return out.returncode, (out.stdout + out.stderr)


def main() -> int:
    for _ in range(50):  # safety cap
        rc, log = run_go_build()
        if rc == 0:
            print('[fix-imports] build clean')
            return 0

        # Find the first unused-import error and fix that file.
        fixed = False
        for line in log.splitlines():
            m = UNUSED.match(line.strip())
            if not m:
                continue
            file = Path(m.group('file'))
            line_no = int(m.group('line'))
            lines = file.read_text(encoding='utf-8').splitlines()
            if 1 <= line_no <= len(lines):
                del lines[line_no - 1]
                file.write_text('\n'.join(lines) + '\n', encoding='utf-8')
                print(f'[fix-imports] removed unused import "{m.group("import")}" from {file.name}:{line_no}')
                fixed = True
                break

        if not fixed:
            print('[fix-imports] no more unused imports; remaining build errors:')
            print(log[:2000])
            return 1

    print('[fix-imports] too many iterations')
    return 1


if __name__ == '__main__':
    sys.exit(main())
