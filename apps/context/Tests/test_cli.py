"""Exercise the compiled Go executable against isolated HTTP servers."""
import contextlib
import http.server
import json
import os
from pathlib import Path
import signal
import subprocess
import tempfile
import threading
import unittest
import urllib.parse
import urllib.request

BINARY = os.environ["MYCTX_BINARY"]


@contextlib.contextmanager
def server(callback):
    class Handler(http.server.BaseHTTPRequestHandler):
        def log_message(self, *args):
            pass

        def handle_request(self):
            body = self.rfile.read(int(self.headers.get("Content-Length", 0)))
            status, headers, response = callback(self.command, self.path, self.headers, body)
            self.send_response(status)
            for key, value in headers.items():
                self.send_header(key, value)
            self.send_header("Content-Length", str(len(response)))
            self.end_headers()
            with contextlib.suppress(BrokenPipeError, ConnectionResetError):
                self.wfile.write(response)

        do_GET = do_POST = do_PATCH = do_DELETE = do_PUT = handle_request

    instance = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Handler)
    thread = threading.Thread(target=instance.serve_forever, daemon=True)
    thread.start()
    try:
        yield f"http://127.0.0.1:{instance.server_port}"
    finally:
        instance.shutdown()
        instance.server_close()
        thread.join()


class CLITests(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        self.config = Path(self.directory.name) / "config.json"
        self.env = {key: value for key, value in os.environ.items()
                    if not key.startswith(("MYCTX_", "CONTEXT_"))}
        self.env["MYCTX_CONFIG"] = str(self.config)

    def run_cli(self, *args, stdin=None, env=None):
        return subprocess.run([BINARY, *args], input=stdin, text=True,
                              capture_output=True, timeout=15, env=env or self.env)

    def test_discovery_and_help_without_configuration(self):
        result = self.run_cli("commands")
        self.assertEqual(result.returncode, 0, result.stderr)
        commands = json.loads(result.stdout)
        self.assertGreaterEqual(len(commands), 42)
        self.assertNotIn("billing checkout", result.stdout)
        self.assertNotIn("billing portal", result.stdout)
        for args in [("help",), ("projects", "--help"), ("--version",)]:
            self.assertEqual(self.run_cli(*args).returncode, 0)
        self.assertIn("projects get", self.run_cli("help", "projects").stdout)
        self.assertNotIn("billing checkout", self.run_cli("help", "projects").stdout)

    def test_named_operations_match_xrpc_contracts(self):
        root = Path(__file__).resolve().parents[3]
        inventory = json.loads((root / "lexicons/operations.json").read_text())
        operations = json.loads(self.run_cli("commands").stdout)
        expected = {entry["nsid"] for entry in inventory["operations"]
                    if entry["authorization"] in ("account", "admin")}
        self.assertEqual({entry["nsid"] for entry in operations if entry.get("nsid")}, expected)
        received = []
        identifiers = {"project_id": ("--id", "project"),
                       "release_id": ("--release-id", "release"),
                       "skill_id": ("--compiled-skill-id", "skill"),
                       "key_id": ("--key-id", "key"),
                       "job_id": ("--job-id", "job")}
        with server(lambda *args: (received.append(args) or (200, {}, b"{}"))) as origin:
            for operation in operations:
                nsid = operation.get("nsid")
                if not nsid:
                    continue
                with self.subTest(command=operation["name"]):
                    schema = json.loads((root / "lexicons" / (nsid + ".json")).read_text())["defs"]["main"]
                    method = "GET" if schema["type"] == "query" else "POST"
                    self.assertEqual(operation["method"], method)
                    self.assertEqual(operation["path"], "/xrpc/" + nsid)
                    self.assertIn("rest_method", operation)
                    self.assertIn("rest_path", operation)
                    params = schema.get("parameters", schema.get("input", {}).get("schema", {}))
                    args = operation["name"].split()
                    required_ids = {}
                    for field, (flag, value) in identifiers.items():
                        if field in params.get("required", []):
                            args += [flag, value]
                            required_ids[field] = value
                    if method == "POST":
                        args += ["--data", "{}"]
                    result = self.run_cli(*args, "--url", origin, env=dict(self.env, MYCTX_TOKEN="method-token"))
                    self.assertEqual(result.returncode, 0, result.stderr)
                    actual_method, path, headers, body = received[-1]
                    self.assertEqual(headers["Authorization"], "Bearer method-token")
                    self.assertNotIn("Cookie", headers)
                    parsed = urllib.parse.urlsplit(path)
                    self.assertEqual((actual_method, parsed.path), (method, "/xrpc/" + nsid))
                    if method == "GET":
                        self.assertEqual(body, b"")
                        expected_query = {key: [value] for key, value in required_ids.items()}
                        if operation["name"] == "github repos":
                            expected_query["provider"] = ["github"]
                        self.assertEqual(urllib.parse.parse_qs(parsed.query), expected_query)
                    else:
                        payload = json.loads(body)
                        for field, value in required_ids.items():
                            self.assertEqual(payload[field], value)

    def test_raw_xrpc_request_uses_explicit_method_and_preserves_query(self):
        received = []
        with server(lambda *args: (received.append(args) or (200, {}, b"{}"))) as origin:
            result = self.run_cli("api", "GET", "/xrpc/dev.mycontextprotocol.project.getProject",
                                  "--query", "project_id=project", "--url", origin + "/api",
                                  env=dict(self.env, MYCTX_TOKEN="method-token"))
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(received[0][:2],
                         ("GET", "/xrpc/dev.mycontextprotocol.project.getProject?project_id=project"))

    def test_transport_choice_never_falls_back_or_mixes_credentials(self):
        for use_token in [False, True]:
            with self.subTest(use_token=use_token):
                received = []
                with server(lambda *args: (received.append(args) or (404, {}, b'{"error":"NotFound"}'))) as origin:
                    self.config.write_text(json.dumps({"profiles": {"default": {
                        "url": origin, "session": "session-secret", "web_url": "https://web.example"}}}))
                    env = dict(self.env)
                    if use_token:
                        env["MYCTX_TOKEN"] = "method-token"
                    result = self.run_cli("projects", "list", env=env)
                self.assertEqual(result.returncode, 1, result.stderr)
                self.assertEqual(len(received), 1)
                method, path, headers, body = received[0]
                self.assertEqual(method, "GET")
                if use_token:
                    self.assertEqual(path, "/xrpc/dev.mycontextprotocol.project.listProjects")
                    self.assertEqual(headers["Authorization"], "Bearer method-token")
                    self.assertNotIn("Cookie", headers)
                else:
                    self.assertEqual(path, "/projects")
                    self.assertEqual(headers["Cookie"], "vapor-session=session-secret")
                    self.assertNotIn("Authorization", headers)

    def test_xrpc_request_injects_ids_into_body_and_retains_exact_numbers(self):
        received = []
        with server(lambda *args: (received.append(args) or (200, {}, args[3]))) as origin:
            result = self.run_cli("skills", "update", "--id", "project", "--release-id", "release",
                                  "--compiled-skill-id", "skill", "--data",
                                  '{"n":123456789012345678901234567890}', "--url", origin,
                                  env=dict(self.env, MYCTX_TOKEN="method-token"))
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(received[0][:2], ("POST", "/xrpc/dev.mycontextprotocol.release.updateCompiledSkill"))
        self.assertEqual(json.loads(received[0][3]), {"project_id": "project", "release_id": "release",
                                                      "skill_id": "skill", "n": 123456789012345678901234567890})
        self.assertIn("123456789012345678901234567890", result.stdout)

    def test_request_path_query_body_and_headers(self):
        received = []
        def response(*request):
            received.append(request)
            return 200, {}, b'{"n":123456789012345678901234567890}'
        with server(response) as origin:
            env = dict(self.env, MYCTX_URL=origin, MYCTX_SESSION="secret", MYCTX_WEB_URL="https://web.example")
            result = self.run_cli("projects", "update", "--id", "project", "--set", 'name="New"',
                                  "--query", "tag=a", "--query", "tag=b", "--compact", env=env)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout.strip(), '{"n":123456789012345678901234567890}')
        method, path, headers, body = received[0]
        self.assertEqual((method, path), ("PATCH", "/projects/project?tag=a&tag=b"))
        self.assertEqual(headers["Cookie"], "vapor-session=secret")
        self.assertEqual(headers["Origin"], "https://web.example")
        self.assertEqual(json.loads(body), {"name": "New"})


    def test_unified_api_prefix_management_and_credential_scope(self):
        received = []
        with server(lambda *args: (received.append(args) or (200, {}, b"{}"))) as origin:
            env = dict(self.env, MYCTX_URL=origin + "/api/", MYCTX_SESSION="secret", MYCTX_WEB_URL="https://web.example")
            result = self.run_cli("projects", "update", "--id", "project", "--set", 'name="Preview"', env=env)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(received[-1][1], "/api/projects/project")
            self.assertEqual(received[-1][2]["Cookie"], "vapor-session=secret")
            self.assertEqual(received[-1][2]["Origin"], "https://web.example")
            self.config.write_text(json.dumps({"profiles": {"default": {"url": origin, "session": "legacy-session"}}}))
            result = self.run_cli("health", "--url", origin + "/api")
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(received[-1][1], "/api/health")
            self.assertNotIn("Cookie", received[-1][2])

    def test_hosted_default_uses_unified_api_base(self):
        shown = self.run_cli("config", "show")
        self.assertEqual(shown.returncode, 0, shown.stderr)
        self.assertEqual(json.loads(shown.stdout)["url"], "https://mycontextprotocol.dev/api")

    def test_response_shapes_and_exit_codes(self):
        for status, body, expected in [(204, b"", 0), (200, b"hello", 0), (401, b'{"reason":"expired"}', 3),
                                       (403, b'{"reason":"denied"}', 4), (500, b"failure", 1)]:
            with self.subTest(status=status), server(lambda *args: (status, {}, body)) as origin:
                result = self.run_cli("health", "--url", origin, "--json")
                self.assertEqual(result.returncode, expected, result.stderr)
                if expected:
                    self.assertEqual(json.loads(result.stderr)["status"], status)
                elif status == 204:
                    self.assertEqual(json.loads(result.stdout), {"ok": True})
                else:
                    self.assertEqual(json.loads(result.stdout), {"result": "hello"})

    def test_invalid_arguments_do_not_request(self):
        received = []
        with server(lambda *args: (received.append(args) or (200, {}, b"{}"))) as origin:
            for args in [("projects", "get"), ("api", "GET", "//evil.example/path"),
                         ("api", "GET", "https://evil.example"), ("api", "GET", "/a\\b"),
                         ("projects", "get", "--id", ".."), ("projects", "get", "--id", "a/b"),
                         ("health", "--data", "{}"), ("projects", "create", "--data", '"\\x"'),
                         ("projects", "create", "--data", "@/missing/myctx/request.json"), ("api", "TRACE", "/health"),
                         ("--timeout", "0s", "health"), ("--unknown", "--json")]:
                result = self.run_cli(*args, "--url", origin)
                self.assertEqual(result.returncode, 2, (args, result.stderr))
        self.assertEqual(received, [])

    def test_redirect_never_forwards_credentials(self):
        leaked = []
        with server(lambda *args: (leaked.append(args) or (200, {}, b"{}"))) as target:
            with server(lambda *args: (302, {"Location": target + "/stolen"}, b"")) as origin:
                result = self.run_cli("health", "--url", origin, env=dict(self.env, MYCTX_SESSION="secret"))
                self.assertEqual(result.returncode, 1)
                install = self.run_cli("github", "install", "--url", origin)
                self.assertEqual(json.loads(install.stdout), {"url": target + "/stolen"})
        self.assertEqual(leaked, [])

    def test_session_import_private_config_logout_and_origin_isolation(self):
        received = []
        with server(lambda *args: (received.append(args) or (200, {}, b'{"ok":true}'))) as origin:
            result = self.run_cli("auth", "login", "--session-stdin", "--url", origin,
                                  "--web-url", "https://web.example", stdin="credential\n")
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(self.config.stat().st_mode & 0o777, 0o600)
            self.assertEqual(json.loads(self.config.read_text())["profiles"]["default"]["session"], "credential")
            self.run_cli("config", "set", "--project", "selected")
            shown = self.run_cli("config", "show")
            self.assertNotIn("credential", shown.stdout)
            self.assertTrue(json.loads(shown.stdout)["authenticated"])
            self.assertEqual(self.run_cli("projects", "get").returncode, 0)
            self.assertIn("/projects/selected", [item[1] for item in received])
            self.assertEqual(self.run_cli("auth", "logout").returncode, 0)
            self.assertEqual(json.loads(self.config.read_text())["profiles"]["default"]["session"], "")
        self.assertEqual(received[0][2]["Cookie"], "vapor-session=credential")
        self.run_cli("config", "set", "--url", "https://other.example")
        profile = json.loads(self.config.read_text())["profiles"]["default"]
        self.assertEqual((profile["session"], profile["project"], profile["web_url"]), ("", "", ""))

    def test_stored_credentials_are_not_sent_to_override_origin(self):
        self.config.write_text(json.dumps({"profiles": {"default": {
            "url": "https://stored.example", "web_url": "https://web.example",
            "session": "private", "project": "saved-project"}}}))
        received = []
        with server(lambda *args: (received.append(args) or (200, {}, b"{}"))) as origin:
            result = self.run_cli("health", "--url", origin)
            self.assertEqual(result.returncode, 0, result.stderr)
            missing = self.run_cli("projects", "get", "--url", origin)
            self.assertEqual(missing.returncode, 2)
        self.assertNotIn("Cookie", received[0][2])
        self.assertNotIn("Origin", received[0][2])
        self.assertEqual(len(received), 1)

    def test_session_import_rejects_invalid_credentials(self):
        for credential in ["", "a;b", "a\nb", "a\x00b", "x" * 8193]:
            result = self.run_cli("auth", "login", "--session-stdin", stdin=credential)
            self.assertEqual(result.returncode, 2, result.stderr)
            self.assertFalse(self.config.exists())

    def test_installer_rejects_invisible_or_relative_destination(self):
        root = Path(__file__).resolve().parents[1]
        for destination in ["relative", str(Path(self.directory.name) / "not-on-path")]:
            result = subprocess.run(["sh", "scripts/install.sh", destination, "test"],
                                    cwd=root, capture_output=True, text=True, env=self.env)
            self.assertNotEqual(result.returncode, 0)
            self.assertFalse(Path(destination).exists())

    def test_profile_environment_and_legacy_precedence(self):
        self.run_cli("config", "set", "--url", "https://stored.example", "--profile", "work")
        env = dict(self.env, MYCTX_PROFILE="work", MYCTX_URL="https://new.example", CONTEXT_URL="https://legacy.example")
        self.assertEqual(json.loads(self.run_cli("config", "show", env=env).stdout)["url"], "https://new.example")
        self.assertEqual(json.loads(self.run_cli("config", "show", "--url", "https://flag.example", env=env).stdout)["url"], "https://flag.example")
        env["MYCTX_URL"] = ""
        self.assertEqual(json.loads(self.run_cli("config", "show", env=env).stdout)["url"], "https://legacy.example")

    def test_stdin_file_data_and_exact_number_merge(self):
        bodies = []
        with server(lambda method, path, headers, body: (bodies.append(body) or (200, {}, body))) as origin:
            for mode in ["stdin", "file"]:
                payload = '{"n":123456789012345678901234567890}'
                path = Path(self.directory.name) / "request.json"
                path.write_text(payload)
                result = self.run_cli("projects", "create", "--url", origin, "--data",
                                      "-" if mode == "stdin" else "@" + str(path),
                                      "--set", 'name="Skills"', stdin=payload)
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertIn("123456789012345678901234567890", result.stdout)
        self.assertEqual(len(bodies), 2)

    def test_readable_errors_strip_terminal_controls(self):
        with server(lambda *args: (403, {}, b'{"reason":"denied\\n\\u001b[31mred"}')) as origin:
            result = self.run_cli("health", "--url", origin)
        self.assertEqual(result.returncode, 4)
        self.assertNotIn("\x1b", result.stderr)
        self.assertIn("Hint:", result.stderr)
        self.assertIn("denied", result.stderr)

    def test_browser_login_nonce_and_handoff(self):
        self.browser_login_nonce_and_handoff("")

    def test_browser_login_uses_explicit_unified_api_prefix(self):
        self.browser_login_nonce_and_handoff("/api")

    def browser_login_nonce_and_handoff(self, prefix):
        callback_done = threading.Event()
        calls = []
        def gateway(method, path, headers, body):
            calls.append((method, path, headers))
            parsed = urllib.parse.urlsplit(path)
            if parsed.path == prefix + "/auth/cli/github":
                callback = urllib.parse.parse_qs(parsed.query)["return_to"][0]
                def handoff():
                    # Invalid nonces must not consume the one successful callback.
                    try:
                        urllib.request.urlopen(callback.replace("state=", "state=wrong") + "&auth_token=bad").close()
                    except urllib.error.HTTPError as error:
                        error.close()
                    urllib.request.urlopen(callback + "&auth_token=handoff").read()
                    callback_done.set()
                threading.Thread(target=handoff, daemon=True).start()
                return 302, {"Location": "https://github.com/login/oauth/authorize?client_id=client&state=signed"}, b""
            if parsed.path == prefix + "/auth/confirm":
                self.assertEqual(urllib.parse.parse_qs(parsed.query), {"token": ["handoff"]})
                return 200, {"Set-Cookie": "vapor-session=logged-in; Path=/; HttpOnly", "X-Context-Frontend-Origin": "https://web.example"}, b"{}"
            return 200, {}, b'{"id":"account"}'
        with server(gateway) as origin:
            result = self.run_cli("auth", "login", "--no-browser", "--url", origin + prefix,
                                  env=dict(self.env, MYCTX_SESSION="old-secret"))
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertTrue(callback_done.wait(1))
        self.assertNotIn("Cookie", calls[0][2])
        self.assertEqual(calls[-1][2]["Cookie"], "vapor-session=logged-in")
        self.assertEqual(json.loads(self.config.read_text())["profiles"]["default"]["session"], "logged-in")
        self.assertEqual(urllib.parse.urlsplit(calls[-1][1]).path, prefix + "/auth/me")
        self.assertTrue(all(urllib.parse.urlsplit(call[1]).path.startswith(prefix + "/auth/") for call in calls))

    def test_browser_preflight_rejects_invalid_gateway(self):
        cases = [(404, {}), (200, {}), (302, {"Location": "https://evil.example/steal"}),
                 (302, {"Location": "https://github.com/login/oauth/authorize?client_id=c"})]
        for status, headers in cases:
            with self.subTest(status=status, headers=headers), server(lambda *args: (status, headers, b"{}")) as origin:
                result = self.run_cli("auth", "login", "--no-browser", "--url", origin)
                self.assertEqual(result.returncode, 1)
                self.assertNotIn("Open this URL", result.stderr)
                self.assertFalse(self.config.exists())

    def test_browser_login_cancellation(self):
        with server(lambda *args: (302, {"Location": "https://github.com/login/oauth/authorize?client_id=c&state=s"}, b"")) as origin:
            process = subprocess.Popen([BINARY, "auth", "login", "--no-browser", "--url", origin],
                                       env=self.env, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
            self.addCleanup(lambda: process.kill() if process.poll() is None else None)
            self.assertIn("Open this URL", process.stderr.readline())
            process.send_signal(signal.SIGINT)
            stdout, stderr = process.communicate(timeout=5)
            self.assertEqual(process.returncode, 130, stderr)
            self.assertFalse(self.config.exists())

    def test_tangled_and_review_bound_publication_aliases(self):
        received = []
        def response(*request):
            received.append(request)
            return 202, {"Content-Type": "application/json"}, b'{"status":"pending","digest":"reviewed"}'
        with server(response) as origin:
            env = dict(self.env, MYCTX_URL=origin + "/api", MYCTX_SESSION="secret")
            result = self.run_cli("tangled", "connect", "--project", "project", "--data",
                                  '{"repo_owner":"did:plc:owner","repo_name":"private","branch":"main","is_private":true}', env=env)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(received[-1][:2], ("POST", "/api/projects/project/connect-repo"))
            self.assertEqual(json.loads(received[-1][3])["provider"], "tangled")
            self.assertTrue(json.loads(received[-1][3])["is_private"])
            for command, method, path, data in [
                ("review", "GET", "/api/projects/project/atproto/publication", None),
                ("publish", "POST", "/api/projects/project/atproto/publication", '{"review_digest":"reviewed","public":true,"telemetry":false}'),
                ("reconcile", "POST", "/api/projects/project/atproto/reconcile", None),
                ("conflict", "GET", "/api/projects/project/atproto/conflict", None),
                ("discard", "POST", "/api/projects/project/atproto/conflict", '{"intent_id":"reviewed-intent","expected_current_cid":"current","action":"discard"}'),
            ]:
                args = ["publication", command, "--project", "project"]
                if data is not None:
                    args += ["--data", data]
                result = self.run_cli(*args, env=env)
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertEqual(received[-1][:2], (method, path))
                self.assertEqual(json.loads(result.stdout)["status"], "pending")
                if data is not None:
                    self.assertEqual(json.loads(received[-1][3]), json.loads(data))
            before = len(received)
            result = self.run_cli("tangled", "connect", "--project", "project", "--data", '{"provider":"github"}', env=env)
            self.assertEqual(result.returncode, 2, result.stderr)
            self.assertEqual(len(received), before)


if __name__ == "__main__":
    unittest.main()
