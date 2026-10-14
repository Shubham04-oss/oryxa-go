"""The sandbox's Python kernel (live-layer A12).

One long-lived interpreter per sandbox, so what one sandbox_execute_code
call defines (a variable, an import, a DataFrame) is there for the next, as
in AgentCore's code interpreter. SandboxDO reaches it over HTTP on
KERNEL_PORT through the container's TCP port, one run at a time:

  POST /exec {"code", "timeoutMs", "stdoutLimit", "stderrLimit"}
       -> {"stdout", "stderr", "exitCode", "timedOut", "truncated", "durationMs"}
  POST /reset   forget everything defined so far
  GET  /health

Output is captured at the file descriptors, so a subprocess's output is in
it too. A run past its timeout can't be stopped inside the interpreter, so
the kernel answers and exits; start.sh starts a fresh one, and the next run
finds its state gone, which SandboxDO reports.
"""

import ast
import json
import os
import sys
import tempfile
import threading
import time
import traceback
from http.server import BaseHTTPRequestHandler
from socketserver import ThreadingTCPServer

KERNEL_PORT = int(os.environ.get("ORYXA_PY_KERNEL_PORT", "8899"))
WORKSPACE = os.environ.get("ORYXA_WORKSPACE", "/workspace")
MAX_BODY = 2 << 20

_lock = threading.Lock()
_namespace = {}


def _fresh():
    _namespace.clear()
    _namespace.update({"__name__": "__main__", "__builtins__": __builtins__})


_fresh()


def _compile(code):
    """Compiles a cell, echoing a final bare expression the way a notebook
    does: `df.head()` alone on the last line shows its value."""
    tree = ast.parse(code, "<cell>", "exec")
    if tree.body and isinstance(tree.body[-1], ast.Expr):
        last = tree.body.pop()
        echo = ast.Expr(
            ast.Call(
                func=ast.Name("__oryxa_echo__", ast.Load()),
                args=[last.value],
                keywords=[],
            )
        )
        tree.body.append(ast.copy_location(echo, last))
        ast.fix_missing_locations(tree)
    return compile(tree, "<cell>", "exec")


def _echo(value):
    if value is not None:
        print(repr(value))


def _read_capped(handle, limit):
    handle.flush()
    os.fsync(handle.fileno())
    handle.seek(0)
    data = handle.read(limit + 1)
    truncated = len(data) > limit
    return data[:limit].decode("utf-8", "replace"), truncated


def run(code, timeout_ms, stdout_limit, stderr_limit):
    started = time.monotonic()
    out = tempfile.TemporaryFile()
    err = tempfile.TemporaryFile()
    sys.stdout.flush()
    sys.stderr.flush()
    saved_out, saved_err = os.dup(1), os.dup(2)
    os.dup2(out.fileno(), 1)
    os.dup2(err.fileno(), 2)
    result = {"exitCode": 0}

    def cell():
        try:
            _namespace["__oryxa_echo__"] = _echo
            exec(_compile(code), _namespace)
        except SystemExit as stop:
            result["exitCode"] = stop.code if isinstance(stop.code, int) else 1
        except BaseException:  # noqa: BLE001 - the cell's error is its output
            kind, value, trace = sys.exc_info()
            # Start at the cell: the kernel's own frame is noise.
            traceback.print_exception(kind, value, trace.tb_next if trace else None)
            result["exitCode"] = 1
        finally:
            sys.stdout.flush()
            sys.stderr.flush()

    worker = threading.Thread(target=cell, daemon=True)
    worker.start()
    worker.join(max(timeout_ms, 1) / 1000)
    timed_out = worker.is_alive()

    os.dup2(saved_out, 1)
    os.dup2(saved_err, 2)
    os.close(saved_out)
    os.close(saved_err)
    stdout, out_truncated = _read_capped(out, stdout_limit)
    stderr, err_truncated = _read_capped(err, stderr_limit)
    return {
        "stdout": stdout,
        "stderr": stderr,
        "exitCode": -1 if timed_out else int(result["exitCode"] or 0),
        "timedOut": timed_out,
        "truncated": out_truncated or err_truncated,
        "durationMs": int((time.monotonic() - started) * 1000),
    }


class Handler(BaseHTTPRequestHandler):
    def log_message(self, *args):  # noqa: D102 - quiet: stderr is the cell's
        pass

    def _answer(self, status, body):
        encoded = json.dumps(body).encode()
        self.send_response(status)
        self.send_header("content-type", "application/json")
        self.send_header("content-length", str(len(encoded)))
        self.end_headers()
        self.wfile.write(encoded)
        self.wfile.flush()

    def do_GET(self):  # noqa: N802
        if self.path == "/health":
            self._answer(200, {"ok": True, "pid": os.getpid()})
        else:
            self._answer(404, {"error": "not found"})

    def do_POST(self):  # noqa: N802
        length = int(self.headers.get("content-length") or 0)
        if length > MAX_BODY:
            self._answer(413, {"error": "too large"})
            return
        try:
            body = json.loads(self.rfile.read(length) or b"{}")
        except ValueError:
            self._answer(400, {"error": "not JSON"})
            return
        if self.path == "/reset":
            with _lock:
                _fresh()
            self._answer(200, {"reset": True})
            return
        if self.path != "/exec":
            self._answer(404, {"error": "not found"})
            return
        with _lock:
            outcome = run(
                str(body.get("code", "")),
                int(body.get("timeoutMs", 120000)),
                int(body.get("stdoutLimit", 192 << 10)),
                int(body.get("stderrLimit", 64 << 10)),
            )
            self._answer(200, outcome)
            if outcome["timedOut"]:
                # The cell is still running and can't be stopped from here:
                # leave, and let start.sh start a fresh kernel.
                os._exit(3)


def main():
    os.makedirs(WORKSPACE, exist_ok=True)
    os.chdir(WORKSPACE)
    sys.path.insert(0, WORKSPACE)
    # socketserver, not http.server's HTTPServer: that one looks up the
    # container's hostname at startup, and on Cloudflare the hostname is 64
    # characters, one more than a DNS label, so it fails and the kernel never
    # listens.
    ThreadingTCPServer.allow_reuse_address = True
    ThreadingTCPServer.daemon_threads = True
    server = ThreadingTCPServer(("0.0.0.0", KERNEL_PORT), Handler)
    server.serve_forever()


if __name__ == "__main__":
    main()
