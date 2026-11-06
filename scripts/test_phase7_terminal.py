#!/usr/bin/env python3
"""Local mock-provider PTY smoke test. No API credentials or paid calls needed.

Usage: go build -race -o /tmp/go-rig-phase7 ./cmd/harness
       python3 scripts/test_phase7_terminal.py /tmp/go-rig-phase7
"""
import contextlib
import fcntl
import http.server
import json
import os
from pathlib import Path
import pty
import select
import signal
import struct
import subprocess
import sys
import tempfile
import termios
import threading
import time

requests = []
protocol_errors = []


class Model(http.server.BaseHTTPRequestHandler):
    def log_message(self, *_):
        pass

    def do_POST(self):
        request = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
        requests.append(request)
        pending = set()
        for m in request["messages"]:
            if m["role"] == "assistant":
                if pending:
                    protocol_errors.append("assistant before tool results")
                pending.update(c["id"] for c in m.get("tool_calls", []))
            elif m["role"] == "tool":
                if m["tool_call_id"] not in pending:
                    protocol_errors.append("orphan result")
                pending.discard(m["tool_call_id"])
            elif m["role"] == "user" and pending:
                protocol_errors.append("user before tool results")
        if pending:
            protocol_errors.append("unfinished tool batch")
        last_user = max(i for i, m in enumerate(request["messages"]) if m["role"] == "user")
        prompt = request["messages"][last_user]["content"]
        completed = sum(m["role"] == "tool" for m in request["messages"][last_user + 1:])
        delta = {"role": "assistant", "content": "ACK:" + prompt}
        action = None
        if prompt == "work" and completed < 5:
            action = [
                ("write", {"path": "note", "content": "initial"}),
                ("edit", {"path": "note", "oldText": "initial", "newText": "updated"}),
                ("read", {"path": "note"}),
                ("bash", {"command": "printf verified"}),
                ("read", {"path": "note"}),
            ][completed]
        elif prompt in ("pause work", "interrupt budget", "print work") and completed < 3:
            action = ("read", {"path": "note"})
        elif prompt in ("interrupt approval", "external interrupt", "eof approval") and completed == 0:
            action = ("write", {"path": "must-not-exist", "content": "denied"})
        if action:
            name, arguments = action
            delta = {"role": "assistant", "tool_calls": [{"index": 0, "id": f"call-{len(requests)}", "type": "function", "function": {"name": name, "arguments": json.dumps(arguments)}}]}
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.end_headers()
        try:
            packet = {"choices": [{"index": 0, "delta": delta, "finish_reason": None}]}
            self.wfile.write(("data: " + json.dumps(packet) + "\n\n").encode())
            finish = "tool_calls" if action else "stop"
            packet = {"choices": [{"index": 0, "delta": {}, "finish_reason": finish}]}
            self.wfile.write(("data: " + json.dumps(packet) + "\n\ndata: [DONE]\n\n").encode())
            self.wfile.flush()
        except (BrokenPipeError, ConnectionResetError):
            pass


class Terminal:
    def __init__(self, binary, base, cwd, root, extra=(), print_output=False):
        self.master, self.slave = pty.openpty()
        fcntl.ioctl(self.slave, termios.TIOCSWINSZ, struct.pack("HHHH", 30, 110, 0, 0))
        self.original = termios.tcgetattr(self.slave)
        self.buffer = self.transcript = b""
        args = [binary, "--api-key", "test-key", "--base-url", base, "--session-dir", root, "--max-turns", "2", *extra]
        # Keep the controlling terminal alive until restoration is checked.
        wrapper = ["/bin/sh", "-c", 'trap "" INT; "$@"; code=$?; printf "\\nHARNESS_EXIT:%s\\n" "$code" >&2; read -r release; exit "$code"', "phase7-terminal", *args]
        self.proc = subprocess.Popen(wrapper, cwd=cwd, stdin=self.slave, stdout=subprocess.PIPE if print_output else self.slave, stderr=self.slave, start_new_session=True, preexec_fn=lambda: fcntl.ioctl(0, termios.TIOCSCTTY, 0))

    def expect(self, token, timeout=8):
        deadline = time.monotonic() + timeout
        while token not in self.buffer:
            remaining = deadline - time.monotonic()
            if remaining <= 0:
                raise AssertionError(f"missing {token!r}; tail={self.transcript[-2500:]!r}")
            ready, _, _ = select.select([self.master], [], [], remaining)
            if ready:
                data = os.read(self.master, 65536)
                self.buffer += data
                self.transcript += data
        before, self.buffer = self.buffer.split(token, 1)
        return before

    def send(self, text):
        os.write(self.master, text if isinstance(text, bytes) else text.encode())

    def prompt(self):
        self.expect(b"you> ")

    def decision(self, answer="y"):
        self.expect(b"Allow? [y/N] ")
        self.send(answer + "\r")

    def exit(self, code=0):
        self.expect(f"HARNESS_EXIT:{code}".encode())
        assert termios.tcgetattr(self.slave) == self.original, "terminal settings were not restored"
        assert b"DATA RACE" not in self.transcript, self.transcript
        self.send("release\n")
        deadline = time.monotonic() + 5
        while self.proc.poll() is None and time.monotonic() < deadline:
            ready, _, _ = select.select([self.master], [], [], .05)
            if ready:
                with contextlib.suppress(OSError):
                    self.transcript += os.read(self.master, 65536)
        assert self.proc.wait(timeout=1) == code

    def close(self):
        if self.proc.poll() is None:
            os.killpg(self.proc.pid, signal.SIGKILL)
            os.close(self.master)
            self.master = -1
            self.proc.wait(timeout=3)
        if self.master >= 0:
            os.close(self.master)
        os.close(self.slave)
        if self.proc.stdout:
            self.proc.stdout.close()


def main():
    binary = os.path.abspath(sys.argv[1])
    server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Model)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    base = f"http://127.0.0.1:{server.server_port}"
    with tempfile.TemporaryDirectory(prefix="go-rig-phase7-") as cwd:
        root = os.path.join(cwd, "sessions")
        Path(cwd, ".git").mkdir()
        Path(cwd, "AGENTS.md").write_text("TERMINAL_PROJECT_INSTRUCTION\n")
        terminal = Terminal(binary, base, cwd, root, ["--approval", "ask", "--path-scope", "workspace"])
        try:
            terminal.expect(b"Trust automatic project instructions?")
            terminal.decision()
            terminal.expect(b"Type /help for commands."); terminal.prompt()
            terminal.send("work\r")
            terminal.expect(b'Allow tool "write"?'); terminal.decision()
            terminal.expect(b'Allow tool "edit"?'); terminal.decision()
            terminal.expect(b"Continue for another 2 turns?"); terminal.decision("invalid")
            terminal.expect(b"Enter y or n."); terminal.decision()
            terminal.expect(b'Allow tool "bash"?'); terminal.decision("n")
            terminal.expect(b"Continue for another 2 turns?"); terminal.decision()
            terminal.expect(b"ACK:work"); terminal.prompt()
            assert Path(cwd, "note").read_text() == "updated"
            assert "TERMINAL_PROJECT_INSTRUCTION" in requests[0]["messages"][0]["content"]
            assert all(m.get("content") not in ("y", "n", "invalid") for req in requests for m in req["messages"] if m["role"] == "user")
            # Confirmation replies must not replace the user's keyboard recall.
            terminal.send(b"\x1b[A\r")
            terminal.expect(b'Allow tool "write"?'); terminal.decision("n")
            terminal.expect(b'Allow tool "edit"?'); terminal.decision("n")
            terminal.expect(b"Continue for another 2 turns?"); terminal.decision("n")
            terminal.expect(b"Paused at the turn checkpoint."); terminal.prompt()
            terminal.send("pause work\r")
            terminal.expect(b"Continue for another 2 turns?"); terminal.decision("n")
            terminal.expect(b"Paused at the turn checkpoint."); terminal.prompt()
            terminal.send("/continue\r"); terminal.expect(b"ACK:pause work"); terminal.prompt()
            terminal.send("interrupt approval\r"); terminal.expect(b'Allow tool "write"?')
            terminal.expect(b"Allow? [y/N] "); terminal.send(b"\x03")
            terminal.expect(b"Canceled."); terminal.prompt()
            terminal.send("external interrupt\r"); terminal.expect(b'Allow tool "write"?')
            terminal.expect(b"Allow? [y/N] "); os.killpg(terminal.proc.pid, signal.SIGINT)
            terminal.expect(b"Canceled."); terminal.prompt()
            terminal.send("interrupt budget\r"); terminal.expect(b"Continue for another 2 turns?")
            terminal.expect(b"Allow? [y/N] "); terminal.send(b"\x03")
            terminal.expect(b"Canceled."); terminal.prompt()
            terminal.send("/continue\r"); terminal.expect(b"ACK:interrupt budget"); terminal.prompt()
            terminal.send("/quit\r"); terminal.exit()
        finally:
            terminal.close()
        resumed = Terminal(binary, base, cwd, root, ["--continue", "--approval", "ask"])
        try:
            resumed.expect(b"Type /help for commands."); resumed.prompt()
            resumed.send("after restart\r"); resumed.expect(b"ACK:after restart"); resumed.prompt()
            assert sum(m.get("content") == "pause work" for m in requests[-1]["messages"] if m["role"] == "user") == 1
            resumed.send("eof approval\r"); resumed.expect(b'Allow tool "write"?')
            resumed.expect(b"Allow? [y/N] "); resumed.send(b"\x04"); resumed.exit()
        finally:
            resumed.close()
        assert not Path(cwd, "must-not-exist").exists()
        printed = Terminal(binary, base, cwd, root, ["--continue", "-p", "print work"], print_output=True)
        try:
            printed.expect(b"Continue for another 2 turns?"); printed.decision()
            printed.exit()
            output = printed.proc.stdout.read()
            assert output == b"ACK:print work\n", output
        finally:
            printed.close()
        for reply in (b"n\r", b"\x04"):
            paused = Terminal(binary, base, cwd, root, ["--continue", "-p", "print work"], print_output=True)
            try:
                paused.expect(b"Continue for another 2 turns?")
                paused.expect(b"Allow? [y/N] ")
                paused.send(reply)
                paused.expect(b"Paused at the turn checkpoint.")
                paused.exit(2)
                assert paused.proc.stdout.read() == b""
            finally:
                paused.close()
        assert not protocol_errors, protocol_errors
        # Check that every saved JSONL entry and parent chain remains valid.
        transcripts = list(Path(root).rglob("*.jsonl"))
        assert len(transcripts) == 1
        entries = [json.loads(line) for line in transcripts[0].read_text().splitlines()]
        parent = None
        for entry in entries[1:]:
            assert entry["parentId"] == parent
            parent = entry["id"]
    server.shutdown()
    print(f"PASS: Phase 7 PTY trust, approvals, repeated renewal, pause/continue, restart, recall, Ctrl+C, external SIGINT, EOF, clean print stdout, and terminal restoration ({len(requests)} mock requests).")


if __name__ == "__main__":
    main()
