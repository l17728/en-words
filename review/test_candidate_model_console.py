import http.client
import http.server
import json
import threading
import unittest
from unittest.mock import patch
import candidate_model_console
from candidate_model_console import MAX_BODY, MODEL, handler


class CandidateProxyTests(unittest.TestCase):
    def setUp(self):
        self.calls = []
        self.upstream_closed = threading.Event()
        upstream_closed = self.upstream_closed
        calls = self.calls
        class Upstream(http.server.BaseHTTPRequestHandler):
            def log_message(self, *_):
                pass
            def do_POST(self):
                body = self.rfile.read(int(self.headers['Content-Length']))
                calls.append((self.path, body, self.headers.get('X-Trace-ID')))
                if body == b'slow':
                    self.connection.settimeout(2)
                    if self.rfile.read(1) == b'':
                        upstream_closed.set()
                    return
                status, output = (429, b'{"error":"busy"}') if body == b'busy' else (200, b'{"choices":[]}')
                self.send_response(status)
                self.send_header('Content-Length', str(len(output)))
                self.end_headers()
                self.wfile.write(output)
        self.upstream = http.server.ThreadingHTTPServer(('127.0.0.1', 0), Upstream)
        self.proxy = http.server.ThreadingHTTPServer(('127.0.0.1', 0), handler(self.upstream.server_port))
        self.threads = []
        for server in (self.upstream, self.proxy):
            thread = threading.Thread(target=server.serve_forever, daemon=True)
            thread.start()
            self.threads.append(thread)

    def tearDown(self):
        for server in (self.proxy, self.upstream):
            server.shutdown()
            server.server_close()
        for thread in self.threads:
            thread.join(2)

    def request(self, method, path, body=None, headers=None):
        client = http.client.HTTPConnection('127.0.0.1', self.proxy.server_port, timeout=3)
        try:
            client.request(method, path, body, headers or {})
            response = client.getresponse()
            return response.status, response.read()
        finally:
            client.close()

    def test_discovery_is_explicit_candidate_identity(self):
        status, body = self.request('GET', '/api/models')
        self.assertEqual(status, 200)
        self.assertEqual(json.loads(body)['services'], [{'id':'vl','model':MODEL,'capabilities':['text']}])
        self.assertEqual(self.calls, [])

    def test_body_and_response_are_not_rewritten(self):
        body = b'{ "model":"candidate", "messages":[{"content":"unicode \\u732b"}], "temperature":0.1 }'
        status, result = self.request('POST', '/api/vl/chat', body, {'X-Trace-ID':'frozen-case'})
        self.assertEqual((status, result), (200, b'{"choices":[]}'))
        self.assertEqual(self.calls, [('/v1/chat/completions', body, 'frozen-case')])

    def test_failure_status_and_body_are_not_masked(self):
        self.assertEqual(self.request('POST', '/api/vl/chat', b'busy'), (429, b'{"error":"busy"}'))

    def test_unknown_routes_and_large_requests_never_reach_upstream(self):
        self.assertEqual(self.request('GET', '/api/status')[0], 404)
        self.assertEqual(self.request('POST', '/api/asr/transcriptions', b'x')[0], 404)
        self.assertEqual(self.request('POST', '/api/vl/chat', b'', {'Content-Length':str(MAX_BODY+1)})[0], 413)
        self.assertEqual(self.calls, [])

    def test_hard_deadline_closes_slow_upstream_and_next_call_runs(self):
        with patch.object(candidate_model_console, 'REQUEST_TIMEOUT', 0.15):
            self.assertEqual(self.request('POST','/api/vl/chat',b'slow')[0],502)
            self.assertTrue(self.upstream_closed.wait(1), 'timed out upstream must receive closed connection')
            self.assertEqual(self.request('POST','/api/vl/chat',b'next')[0],200)


if __name__ == '__main__':
    unittest.main()
