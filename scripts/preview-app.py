"""
Tiny static server for visual smoke testing.

Mounts ./web at /app/ AND proxies /api/* to a real
pchat-server (default localhost:8848). This lets the
front-end run on a different port than the server
(no need to bundle pchat-server's static handler here).

Run from project root:

    python scripts/preview-app.py 8000
    python scripts/preview-app.py 8000 9000   # custom API port

Then open http://127.0.0.1:8000/app/index.html.
"""
import os
import sys
import urllib.request
import urllib.error
from http.server import HTTPServer, BaseHTTPRequestHandler
from urllib.parse import unquote


# Ports — the static port is the first arg, the upstream
# API port is the optional second arg.
STATIC_PORT = int(sys.argv[1]) if len(sys.argv) > 1 else 8000
API_PORT = int(sys.argv[2]) if len(sys.argv) > 2 else 8848


class PreviewHandler(BaseHTTPRequestHandler):
    """Serve /app/* from disk, proxy everything else to the
    upstream server. Health endpoints and other top-level
    paths (no /app/ prefix) all forward through."""

    def translate_path(self, path):
        # Strip the query string so a stray "?demo=1"
        # doesn't break filename matching.
        path = path.split("?", 1)[0]
        path = unquote(path)
        rel = path.lstrip("/")
        if rel.startswith("app/"):
            rel = rel[4:]
        elif rel == "app":
            rel = ""
        return os.path.join(os.getcwd(), rel)

    def do_GET(self):
        if self.path.startswith("/api/") or self.path.startswith("/uploads/"):
            return self._proxy()
        return super().do_GET()

    def do_POST(self):
        return self._proxy()

    def do_PUT(self):
        return self._proxy()

    def do_DELETE(self):
        return self._proxy()

    def do_PATCH(self):
        return self._proxy()

    def _proxy(self):
        """Forward the request (with body) to the upstream
        pchat-server on API_PORT. Returns the response
        verbatim, skipping hop-by-hop headers that confuse
        the server."""
        url = f"http://127.0.0.1:{API_PORT}{self.path}"
        body = None
        if "Content-Length" in self.headers:
            try:
                length = int(self.headers["Content-Length"])
                body = self.rfile.read(length) if length > 0 else None
            except (ValueError, OSError):
                body = None
        # Forward only the headers the upstream cares about.
        skip = {
            "host", "connection", "content-length", "transfer-encoding",
        }
        fwd = {k: v for k, v in self.headers.items() if k.lower() not in skip}
        req = urllib.request.Request(url, data=body, method=self.command, headers=fwd)
        try:
            with urllib.request.urlopen(req, timeout=30) as resp:
                payload = resp.read()
                self.send_response(resp.status)
                for k, v in resp.getheaders():
                    if k.lower() in {"connection", "transfer-encoding", "content-length"}:
                        continue
                    self.send_header(k, v)
                self.send_header("Content-Length", str(len(payload)))
                self.end_headers()
                self.wfile.write(payload)
        except urllib.error.HTTPError as e:
            try:
                payload = e.read()
            except Exception:
                payload = str(e).encode()
            self.send_response(e.code)
            self.send_header("Content-Length", str(len(payload)))
            self.end_headers()
            self.wfile.write(payload)
        except urllib.error.URLError as e:
            msg = f"upstream error: {e.reason}".encode()
            self.send_response(502)
            self.send_header("Content-Type", "text/plain; charset=utf-8")
            self.send_header("Content-Length", str(len(msg)))
            self.end_headers()
            self.wfile.write(msg)

    def log_message(self, fmt, *args):
        # Quiet the access log so the preview server doesn't
        # spam stdout. Uncomment to debug.
        # sys.stderr.write("[preview] " + (fmt % args) + "\n")
        pass


if __name__ == "__main__":
    os.chdir(os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "web"))
    srv = HTTPServer(("127.0.0.1", STATIC_PORT), PreviewHandler)
    print(
        f"serving web/ at http://127.0.0.1:{STATIC_PORT}/app/  "
        f"(API proxied from 127.0.0.1:{API_PORT})",
        flush=True,
    )
    srv.serve_forever()
