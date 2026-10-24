"""Pruebas del cliente. Ejecutar desde la raíz: python3 -m unittest discover client"""
import os
import contextlib
import hashlib
import io
import json
import stat
import threading
import tempfile
import unittest
import urllib.error
from unittest import mock

import dfsha


class ClientTestCase(unittest.TestCase):
    def setUp(self):
        self.folder = tempfile.TemporaryDirectory()
        self.addCleanup(self.folder.cleanup)
        self.session = os.path.join(self.folder.name, "session.json")
        environment = mock.patch.dict(os.environ, {"DFSHA_SESSION": self.session, "DFSHA_TOKEN": ""})
        environment.start()
        self.addCleanup(environment.stop)
        self.client = dfsha.DfshaClient("https://control-1:8000,https://control-2:8000", quiet=True)

    def local(self, name, data=b""):
        path = os.path.join(self.folder.name, name)
        with open(path, "wb") as target:
            target.write(data)
        return path

    @staticmethod
    def plan(data=b"abcdefghijkl", block_size=4):
        return {"path": "/data", "size": len(data), "block_size": block_size,
                "sha256": hashlib.sha256(data).hexdigest(), "blocks": [
                    {"index": i // block_size, "block_id": hashlib.sha256(data[i:i + block_size]).hexdigest(),
                     "size": len(data[i:i + block_size]), "targets": [
                         {"node": "dn1", "url": "https://dn1:9000", "token": "read-capability"},
                         {"node": "dn2", "url": "https://dn2:9000", "token": "read-capability-2"}]}
                    for i in range(0, len(data), block_size)]}


class ControlFailoverTest(ClientTestCase):
    def test_no_content_response_is_a_serializable_empty_object(self):
        response = mock.MagicMock()
        response.status = 204
        response.read.return_value = b""
        response.__enter__.return_value = response
        with mock.patch("dfsha.urllib.request.urlopen", return_value=response):
            self.assertEqual(self.client._request("POST", "https://control-1:8000/files/close"), {})

    def test_plain_http_requires_explicit_insecure_mode(self):
        with mock.patch.dict(os.environ, {"DFSHA_INSECURE": "false"}):
            with self.assertRaisesRegex(ValueError, "HTTPS obligatorio"):
                self.client._request("POST", "http://localhost:8000/auth/login", json_body={"password": "secret"})

    def test_follows_leader_hint_and_remembers_success(self):
        error = dfsha.DfshaHttpError(503, '{"error":"not leader","leader":"https://control-2:8000"}')
        with mock.patch.object(self.client, "_request", side_effect=[error, {"ok": True}]) as request:
            self.assertEqual(self.client._control("GET", "/fs/stat"), {"ok": True})
        self.assertEqual(request.call_count, 2)
        self.assertEqual(self.client.control_url, "https://control-2:8000")

    def test_network_failure_uses_next_control(self):
        with mock.patch.object(self.client, "_request", side_effect=[TimeoutError(), {"ok": True}]) as request:
            self.client._control("GET", "/fs/stat")
        self.assertEqual(request.call_args_list[1].args[1], "https://control-2:8000/fs/stat")

    def test_unauthorized_never_retries_other_control(self):
        with mock.patch.object(self.client, "_request", side_effect=dfsha.DfshaHttpError(401, "expired")) as request:
            with self.assertRaises(dfsha.DfshaHttpError):
                self.client._control("GET", "/fs/stat")
        self.assertEqual(request.call_count, 1)


class SessionAndPathsTest(ClientTestCase):
    def test_cd_validates_and_persists_relative_directory(self):
        with mock.patch.object(self.client, "_control", return_value={"type": "dir"}):
            self.client.cd("/docs")
            self.client.cd("reports")
        restored = dfsha.DfshaClient(quiet=True)
        self.assertEqual(restored.cwd, "/docs/reports")
        self.assertEqual(restored.path("../a.txt"), "/docs/a.txt")
        self.assertEqual(restored.path("/a.txt"), "/a.txt")

    def test_cd_file_does_not_change_cwd(self):
        with mock.patch.object(self.client, "_control", return_value={"type": "file"}):
            with self.assertRaises(ValueError):
                self.client.cd("file")
        self.assertEqual(self.client.cwd, "/")

    def test_login_saves_token_privately_without_printing_it(self):
        output = io.StringIO()
        with mock.patch.object(self.client, "_control", return_value={"token": "session-secret"}):
            with contextlib.redirect_stdout(output):
                response = self.client.login("alice", "password")
        self.assertNotIn("session-secret", output.getvalue())
        self.assertNotIn("token", response)
        with open(self.session, encoding="utf-8") as source:
            self.assertEqual(json.load(source)["token"], "session-secret")
        if os.name != "nt":
            self.assertEqual(stat.S_IMODE(os.stat(self.session).st_mode), 0o600)

    def test_login_change_user_resets_cwd_and_handles(self):
        self.client.cwd, self.client.handles = "/old", {"old": {"mode": "w"}}
        self.client.session["username"] = "alice"
        with mock.patch.object(self.client, "_control", return_value={"token": "new-token"}):
            self.client.login("bob", "password")
        self.assertEqual((self.client.cwd, self.client.handles), ("/", {}))

    def test_rejects_ambiguous_local_windows_paths(self):
        for path in ("a\\b", "a\x00b"):
            with self.assertRaises(ValueError):
                self.client.path(path)


class DownloadSafetyTest(ClientTestCase):
    def test_refreshes_old_read_capabilities_for_same_file_version(self):
        plan = self.plan()
        fresh = json.loads(json.dumps(plan))
        fresh["blocks"][0]["targets"][0]["token"] = "fresh-token"
        with mock.patch("dfsha.time.monotonic", side_effect=[0, 41, 41]):
            resolver = self.client._read_block_resolver("/data", None, plan)
            with mock.patch.object(self.client, "_download_plan", return_value=fresh):
                block = resolver(plan["blocks"][0])
        self.assertEqual(block["targets"][0]["token"], "fresh-token")

    def test_refresh_rejects_mixing_two_file_versions(self):
        plan = self.plan()
        fresh = dict(plan, sha256="0" * 64)
        with mock.patch("dfsha.time.monotonic", side_effect=[0, 41]):
            resolver = self.client._read_block_resolver("/data", None, plan)
            with mock.patch.object(self.client, "_download_plan", return_value=fresh):
                with self.assertRaisesRegex(RuntimeError, "cambió durante"):
                    resolver(plan["blocks"][0])

    def test_corrupt_replica_falls_back_and_uses_only_capability(self):
        block = self.plan()["blocks"][0]
        self.client.token = "user-session"
        with mock.patch.object(self.client, "_request", side_effect=[b"xxxx", b"abcd"]) as request:
            index, data, node = self.client._download_block(block)
        self.assertEqual((index, data, node), (0, b"abcd", "dn2"))
        self.assertEqual(request.call_args_list[0].kwargs["headers"]["Authorization"], "Bearer read-capability")

    def test_failed_full_file_hash_preserves_existing_local_file(self):
        plan = self.plan()
        plan["sha256"] = "0" * 64
        destination = self.local("result.bin", b"previous")
        def download(block):
            return block["index"], b"abcdefghijkl"[block["index"] * 4:(block["index"] + 1) * 4], "dn1"
        with mock.patch.object(self.client, "_download_plan", return_value=plan):
            with mock.patch.object(self.client, "_download_block", side_effect=download):
                with self.assertRaisesRegex(RuntimeError, "archivo completo"):
                    self.client.get("/data", destination)
        with open(destination, "rb") as source:
            self.assertEqual(source.read(), b"previous")
        self.assertFalse(any(name.endswith(".part") for name in os.listdir(self.folder.name)))

    def test_parallel_download_reconstructs_then_atomically_publishes(self):
        plan = self.plan()
        destination = self.local("result.bin", b"previous")
        def download(block):
            return block["index"], b"abcdefghijkl"[block["index"] * 4:(block["index"] + 1) * 4], "dn1"
        with mock.patch.object(self.client, "_download_plan", return_value=plan):
            with mock.patch.object(self.client, "_download_block", side_effect=download):
                self.client.get("/data", destination, workers=3)
        with open(destination, "rb") as source:
            self.assertEqual(source.read(), b"abcdefghijkl")

    def test_range_fetches_only_intersecting_blocks(self):
        destination = self.local("range.bin")
        with mock.patch.object(self.client, "_download_plan", return_value=self.plan()):
            with mock.patch.object(self.client, "_request", side_effect=[b"d", b"ef"]) as request:
                self.client.read("/data", 3, 3, output=destination)
        self.assertEqual(request.call_count, 2)
        self.assertEqual([call.kwargs["headers"]["Range"] for call in request.call_args_list], ["bytes=3-3", "bytes=0-1"])
        with open(destination, "rb") as source:
            self.assertEqual(source.read(), b"def")

    def test_empty_and_past_eof_ranges_do_not_fetch_blocks(self):
        for offset, length in ((2, 0), (100, 10)):
            with mock.patch.object(self.client, "_download_plan", return_value=self.plan()):
                with mock.patch.object(self.client, "_request") as request:
                    result = self.client.read("/data", offset, length, output=self.local("empty.bin"))
            self.assertEqual(result["size"], 0)
            request.assert_not_called()

    def test_invalid_plan_rejected_before_writing(self):
        plan = self.plan()
        plan["blocks"][1]["index"] = 4
        with self.assertRaises(RuntimeError):
            self.client._validate_plan(plan)


class WriteAndUploadTest(ClientTestCase):
    def test_legacy_empty_file_has_one_verified_empty_block(self):
        plan = self.plan(b"")
        plan["blocks"] = [{"index": 0, "size": 0, "block_id": hashlib.sha256(b"").hexdigest(),
                           "targets": [{"node": "dn1", "url": "https://dn1", "token": "read"}]}]
        destination = self.local("legacy-empty.bin", b"previous")
        with mock.patch.object(self.client, "_download_plan", return_value=plan):
            with mock.patch.object(self.client, "_request", return_value=b""):
                self.client.get("/empty", destination)
        self.assertEqual(os.path.getsize(destination), 0)

    def test_upload_renews_lease_and_new_workers_use_fresh_capabilities(self):
        local = self.local("long.bin", b"abcdefgh")
        plan = {"upload_id": "upload", "block_size": 4, "n_blocks": 2, "blocks": [
            {"index": index, "targets": [{"node": "dn1", "url": "https://dn1", "token": "old"}]}
            for index in range(2)]}
        fresh = json.loads(json.dumps(plan))
        for block in fresh["blocks"]:
            block["targets"][0]["token"] = "renewed"
        renewed = threading.Event()
        fresh_blocks = fresh["blocks"]
        def updated_blocks():
            yield from fresh_blocks
            # Se activa después de que dict.update aplicó todos los destinos,
            # aún dentro del lock de renovación: no depende de temporizadores.
            renewed.set()
        fresh["blocks"] = updated_blocks()
        routes = []
        def control(method, route, **kwargs):
            routes.append(route)
            if route.endswith("/plan"):
                return plan
            if route.endswith("/renew"):
                return fresh
            if route.endswith("/commit"):
                return {"ok": True}
            self.fail("Unexpected route " + route)
        def upload(path, block_size, block):
            if block["index"] == 0:
                self.assertTrue(renewed.wait(2), "renewal thread did not run")
            else:
                self.assertEqual(block["targets"][0]["token"], "renewed")
            return {"index": block["index"], "block_id": "hash", "size": 4, "stored_on": ["dn1"]}
        with mock.patch("dfsha.UPLOAD_RENEW_SECONDS", 0.001):
            with mock.patch.object(self.client, "_control", side_effect=control):
                with mock.patch.object(self.client, "_upload_block", side_effect=upload):
                    self.assertEqual(self.client.put(local, "/data", workers=1), {"ok": True})
        self.assertIn("/files/upload/renew", routes)
        self.assertEqual(routes[-1], "/files/upload/commit")

    def test_write_is_copy_on_write_preserving_unmodified_bytes(self):
        self.client.handles["writer"] = {"path": "/data", "mode": "w"}
        patch = self.local("patch.bin", b"XY")
        def get(remote, local, *args, **kwargs):
            with open(local, "wb") as target:
                target.write(b"abcdef")
        def put(local, remote, *args, **kwargs):
            with open(local, "rb") as source:
                self.assertEqual(source.read(), b"abXYef")
            self.assertEqual(remote, "/data")
            self.assertEqual(kwargs["handle"], "writer")
            return {"ok": True}
        with mock.patch.object(self.client, "get", side_effect=get):
            with mock.patch.object(self.client, "put", side_effect=put):
                self.assertEqual(self.client.write("writer", patch, offset=2), {"ok": True})

    def test_write_requires_writer_handle(self):
        with self.assertRaises(ValueError):
            self.client.write("unknown", "irrelevant.bin")

    def test_upload_forwards_json_capabilities_and_reports_real_replicas(self):
        source = self.local("upload.bin", b"abcd")
        targets = self.plan()["blocks"][0]["targets"]
        with mock.patch.object(self.client, "_request", return_value={"stored_on": ["dn1", "dn2"]}) as request:
            result = self.client._upload_block(source, 4, {"index": 0, "targets": targets})
        self.assertEqual(result["stored_on"], ["dn1", "dn2"])
        self.assertEqual(json.loads(request.call_args.kwargs["headers"]["X-Forward-To"]), targets[1:])
        self.assertEqual(request.call_args.kwargs["headers"]["Authorization"], "Bearer read-capability")

    def test_upload_does_not_invent_replica_confirmation(self):
        source = self.local("upload.bin", b"abcd")
        with mock.patch.object(self.client, "_request", return_value={}):
            with self.assertRaisesRegex(RuntimeError, "no confirmó"):
                self.client._upload_block(source, 4, {"index": 0, "targets": self.plan()["blocks"][0]["targets"]})

    def test_empty_file_download_is_verified_and_created(self):
        destination = self.local("empty.bin", b"previous")
        with mock.patch.object(self.client, "_download_plan", return_value=self.plan(b"")):
            result = self.client.get("/empty", destination)
        self.assertEqual(os.path.getsize(destination), 0)
        self.assertEqual(result["sha256"], hashlib.sha256(b"").hexdigest())


class NetworkRetriesTest(unittest.TestCase):
    def setUp(self):
        patcher = mock.patch("dfsha.time.sleep")
        self.sleep = patcher.start()
        self.addCleanup(patcher.stop)
        stderr = mock.patch("sys.stderr")
        stderr.start()
        self.addCleanup(stderr.stop)

    def test_retries_network_failures_with_growing_waits(self):
        action = mock.Mock(side_effect=[urllib.error.URLError("caído"), ConnectionResetError(), "ok"])
        self.assertEqual(dfsha.with_network_retries(action, "bloque 0"), "ok")
        self.assertEqual(action.call_count, 3)
        self.assertEqual([c.args[0] for c in self.sleep.call_args_list], [1, 2])

    def test_gives_up_after_three_retries(self):
        action = mock.Mock(side_effect=TimeoutError())
        with self.assertRaises(TimeoutError):
            dfsha.with_network_retries(action, "bloque 0")
        self.assertEqual(action.call_count, 4)  # intento inicial + 3 reintentos
        self.assertEqual([c.args[0] for c in self.sleep.call_args_list], [1, 2, 4])

    def test_does_not_retry_server_errors(self):
        action = mock.Mock(side_effect=dfsha.DfshaHttpError(400, "petición inválida"))
        with self.assertRaises(dfsha.DfshaHttpError):
            dfsha.with_network_retries(action, "bloque 0")
        self.assertEqual(action.call_count, 1)
        self.sleep.assert_not_called()


class ExpiredReservationTest(unittest.TestCase):
    def setUp(self):
        stderr = mock.patch("sys.stderr")
        stderr.start()
        self.addCleanup(stderr.stop)
        self.client = dfsha.DfshaClient("http://controlnode:8000")

    def test_requests_a_new_plan_once_when_commit_returns_410(self):
        with mock.patch.object(self.client, "_put_once", side_effect=[dfsha.DfshaHttpError(410, "reserva vencida"), None]) as put_once:
            self.client.put("local.bin", "/remoto.bin")
        self.assertEqual(put_once.call_count, 2)

    def test_other_server_errors_are_not_retried(self):
        with mock.patch.object(self.client, "_put_once", side_effect=dfsha.DfshaHttpError(409, "reserva activa")) as put_once:
            with self.assertRaises(dfsha.DfshaHttpError):
                self.client.put("local.bin", "/remoto.bin")
        self.assertEqual(put_once.call_count, 1)


class BlockFileIoTest(unittest.TestCase):
    def test_blocks_written_out_of_order_rebuild_the_file(self):
        original = os.urandom(25)
        block_size = 10
        with tempfile.TemporaryDirectory() as tmp:
            source = os.path.join(tmp, "origen.bin")
            target = os.path.join(tmp, "destino.bin")
            with open(source, "wb") as f:
                f.write(original)
            with open(target, "wb") as f:
                f.truncate(len(original))

            for index in (2, 0, 1):  # llegan en desorden, como en la descarga paralela
                data = dfsha.read_block(source, index, block_size)
                dfsha.write_block(target, index, block_size, data)

            with open(target, "rb") as f:
                self.assertEqual(f.read(), original)


if __name__ == "__main__":
    unittest.main()
