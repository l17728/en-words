"""Isolated loopback adapter for unchanged production evaluation requests.

No prompt, sampling, response or model-output rewriting. The llama server must
be started separately with the same alias advertised here.
"""
import argparse
import http.client
import http.server
import json
import socket
import threading

MODEL = "qwen3.5-9b-q4_k_m-candidate"
MAX_BODY = 1024 * 1024
REQUEST_TIMEOUT = 8.0  # Same hard text deadline as the production adapter.


def handler(upstream_port):
    class Handler(http.server.BaseHTTPRequestHandler):
        def log_message(self, *_):
            pass  # Never log prompt text, response text or headers.

        def reply(self, status, body, kind="application/json"):
            self.send_response(status)
            self.send_header("Content-Type", kind)
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            try:
                self.wfile.write(body)
            except (BrokenPipeError, ConnectionResetError):
                pass

        def do_GET(self):
            if self.path != "/api/models":
                return self.reply(404, b'{}')
            self.reply(200, json.dumps({"services": [{"id": "vl", "model": MODEL,
                                                       "capabilities": ["text"]}]}).encode())

        def do_POST(self):
            if self.path != "/api/vl/chat":
                return self.reply(404, b'{}')
            if self.headers.get("Transfer-Encoding"):
                return self.reply(400, b'{"error":"unsupported_transfer_encoding"}')
            try:
                size = int(self.headers.get("Content-Length", "-1"))
            except ValueError:
                size = -1
            if size < 1 or size > MAX_BODY:
                return self.reply(413, b'{"error":"invalid_request_size"}')
            self.connection.settimeout(10)
            connection = http.client.HTTPConnection("127.0.0.1", upstream_port, timeout=REQUEST_TIMEOUT)
            expired = threading.Event()
            active_peer = [None]
            def cancel_upstream():
                expired.set()
                peer = active_peer[0] or connection.sock
                if peer:
                    try:
                        peer.shutdown(socket.SHUT_RDWR)
                    except OSError:
                        pass
                connection.close()
            deadline = threading.Timer(REQUEST_TIMEOUT, cancel_upstream)
            try:
                body = self.rfile.read(size)
                if len(body) != size:
                    return self.reply(400, b'{"error":"incomplete_body"}')
                headers = {"Content-Type": "application/json"}
                trace = self.headers.get("X-Trace-ID")
                if trace:
                    headers["X-Trace-ID"] = trace
                deadline.start()
                connection.connect()
                active_peer[0] = connection.sock
                if expired.is_set():
                    raise TimeoutError('candidate deadline')
                connection.request("POST", "/v1/chat/completions", body, headers)
                response = connection.getresponse()
                output = response.read(MAX_BODY + 1)
                if len(output) > MAX_BODY:
                    return self.reply(502, b'{"error":"oversized_upstream_response"}')
                self.reply(response.status, output, response.getheader("Content-Type", "application/json"))
            except (OSError, http.client.HTTPException):
                self.reply(502, b'{"error":"candidate_transport_failure"}')
            finally:
                deadline.cancel()
                connection.close()
    return Handler


if __name__ == "__main__":
    p = argparse.ArgumentParser()
    p.add_argument("--port", type=int, default=18090)
    p.add_argument("--upstream-port", type=int, default=18000)
    args = p.parse_args()
    if not all(1024 <= v <= 65535 for v in (args.port, args.upstream_port)) or args.port == args.upstream_port:
        p.error("distinct unprivileged loopback ports required")
    with http.server.ThreadingHTTPServer(("127.0.0.1", args.port), handler(args.upstream_port)) as server:
        server.serve_forever()
