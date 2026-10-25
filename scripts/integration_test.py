#!/usr/bin/env python3
"""Prueba de integración: levanta 3 ControlNodes y 4 DataNodes como procesos
reales con TLS en esta máquina, ejecuta los escenarios y simula fallos.

Primero compilar con scripts/build.py. No necesita Docker. Las claves y los
datos se generan en una carpeta temporal que se borra al final; el resultado
queda en results/integration.json sin secretos.
"""
from __future__ import annotations

import argparse
import base64
from datetime import datetime
import hashlib
import hmac
import json
import os
from pathlib import Path
import random
import shutil
import socket
import ssl
import subprocess
import sys
import tempfile
import time
import traceback
import urllib.error
import urllib.request

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / "client"))
from dfsha import DfshaClient, DfshaHttpError, NETWORK_ERRORS, file_sha256  # noqa: E402

MIB = 1024 * 1024


def free_ports(count):
    # Se mantienen abiertos juntos para no elegir dos veces el mismo puerto.
    sockets = []
    try:
        for _ in range(count):
            sock = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
            sock.bind(("127.0.0.1", 0))
            sockets.append(sock)
        return [sock.getsockname()[1] for sock in sockets]
    finally:
        for sock in sockets:
            sock.close()


def payload(path, size, seed):
    rng = random.Random(seed)
    remaining = size
    with path.open("wb") as target:
        while remaining:
            amount = min(MIB, remaining)
            target.write(rng.randbytes(amount))
            remaining -= amount
    return file_sha256(path)


def eventually(action, timeout=40, description="condition"):
    deadline = time.monotonic() + timeout
    last = None
    while time.monotonic() < deadline:
        try:
            value = action()
            if value:
                return value
        except (OSError, RuntimeError, ValueError) as error:
            last = error
        time.sleep(0.2)
    raise AssertionError(f"timeout waiting for {description}: {last or 'condition false'}")


def expect_http(code, action):
    try:
        action()
    except DfshaHttpError as error:
        assert error.status == code, f"expected HTTP {code}, received {error.status}: {error}"
        return
    raise AssertionError(f"expected HTTP {code}, operation succeeded")


class NativeCluster:
    def __init__(self, bin_dir, workdir):
        self.bin_dir, self.workdir = bin_dir, workdir
        self.secret_dir = workdir / ".secrets"
        self.processes, self.logs, self.envs = {}, {}, {}
        ports = free_ports(10)
        self.controls = {f"cn-{i+1:02}": f"https://localhost:{ports[i]}" for i in range(3)}
        self.rafts = {f"cn-{i+1:02}": f"127.0.0.1:{ports[i+3]}" for i in range(3)}
        self.datas = {f"dn-{i+1:02}": f"https://localhost:{ports[i+6]}" for i in range(4)}
        self.credentials = {}
        self.redactions = []

    def binary(self, name):
        suffix = ".exe" if os.name == "nt" else ""
        path = self.bin_dir / (name + suffix)
        if not path.is_file():
            raise FileNotFoundError(f"missing {path}; execute scripts/build.py first")
        return str(path)

    def initialize(self):
        for name in ("admin", "controlnode", "datanode"):
            self.binary(name)
        subprocess.run([self.binary("admin"), "--out", str(self.secret_dir)], check=True, capture_output=True, text=True)
        subprocess.run([self.binary("admin"), "--out", str(self.secret_dir), "--add-user", "--user", "other"], check=True, capture_output=True, text=True)
        for line in (self.secret_dir / "credentials.txt").read_text().splitlines():
            user, password = line.split(" ", 1)
            self.credentials[user] = password
            self.redactions.append(password)
        for name in ("cluster-key", "signing-key", "encryption-key"):
            self.redactions.append((self.secret_dir / name).read_text().strip())
        env = os.environ.copy()
        # La prueba no hereda sesiones ni claves de la terminal que la ejecuta.
        for name in ("CLUSTER_KEY", "SIGNING_KEY", "ENCRYPTION_KEY", "DFSHA_TOKEN", "DFSHA_SESSION", "DFSHA_CONTROL", "DFSHA_CONTROLS"):
            env.pop(name, None)
        env.update({
            "CLUSTER_KEY_FILE": str(self.secret_dir / "cluster-key"),
            "SIGNING_KEY_FILE": str(self.secret_dir / "signing-key"),
            "ENCRYPTION_KEY_FILE": str(self.secret_dir / "encryption-key"),
            "USERS_FILE": str(self.secret_dir / "users.json"),
            "TLS_CERT": str(self.secret_dir / "cluster.crt"),
            "TLS_KEY": str(self.secret_dir / "cluster.key"),
            "TLS_CA": str(self.secret_dir / "ca.crt"),
            "DFSHA_INSECURE": "false",
            "HEARTBEAT_MS": "300", "HEARTBEAT_MISSES": "3",
            "MAINTENANCE_MS": "500", "REPORT_MS": "1000",
            "CAPACITY_BYTES": str(128 * MIB), "GC_GRACE_SECONDS": "120",
            "WRITE_LEASE_SECONDS": "60",
        })
        peers = ",".join(f"{node}@{self.rafts[node]}@{url}" for node, url in self.controls.items())
        for node, url in self.controls.items():
            self.envs[node] = dict(env, NODE_ID=node, CN_ADDR=":" + url.rsplit(":", 1)[1], CN_PEERS=peers,
                                   RAFT_ADDR=self.rafts[node], RAFT_ADVERTISE=self.rafts[node],
                                   METADATA_DIR=str(self.workdir / node))
        for node, url in self.datas.items():
            self.envs[node] = dict(env, NODE_ID=node, DN_ADDR=":" + url.rsplit(":", 1)[1], ADVERTISE_URL=url,
                                   CONTROL_URLS=",".join(self.controls.values()), STORAGE_DIR=str(self.workdir / node))
        self.ssl_context = ssl.create_default_context(cafile=str(self.secret_dir / "ca.crt"))
        for node in self.controls:
            self.start(node)
        for node in list(self.datas)[:3]:
            self.start(node)
        for node in list(self.controls) + list(self.datas)[:3]:
            eventually(lambda node=node: self.health(node), description=f"{node} TLS health")

    def start(self, node):
        if node in self.processes and self.processes[node].poll() is None:
            raise RuntimeError(f"already running: {node}")
        folder = self.workdir / "logs"
        folder.mkdir(exist_ok=True)
        handle = (folder / (node + ".log")).open("ab", buffering=0)
        self.logs[node] = handle
        binary = "controlnode" if node.startswith("cn-") else "datanode"
        self.processes[node] = subprocess.Popen([self.binary(binary)], cwd=str(ROOT), env=self.envs[node],
                                               stdout=handle, stderr=subprocess.STDOUT)

    def stop(self, node):
        process = self.processes.get(node)
        if process and process.poll() is None:
            process.terminate()
            try:
                process.wait(timeout=8)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait(timeout=8)
        handle = self.logs.pop(node, None)
        if handle:
            handle.close()

    def close(self):
        for node in list(self.processes):
            self.stop(node)
        # Un proceso que no arrancó puede dejar abierto su archivo de log.
        for handle in self.logs.values():
            handle.close()
        self.logs.clear()

    def url(self, node):
        return self.controls.get(node) or self.datas[node]

    def http(self, method, url, data=None, internal=False, timeout=8):
        headers = {}
        if internal:
            headers["X-Cluster-Key"] = (self.secret_dir / "cluster-key").read_text().strip()
        request = urllib.request.Request(url, data=data, method=method, headers=headers)
        with urllib.request.urlopen(request, context=self.ssl_context, timeout=timeout) as response:
            raw = response.read()
            return json.loads(raw) if "application/json" in response.headers.get("Content-Type", "") else raw

    def health(self, node):
        process = self.processes[node]
        if process.poll() is not None:
            raise RuntimeError(f"{node} exited with {process.returncode}")
        return self.http("GET", self.url(node) + "/health")

    def inventory(self, node):
        return self.http("GET", self.datas[node] + "/inventory", internal=True)

    def live_data(self):
        return [node for node in self.datas if node in self.processes and self.processes[node].poll() is None]

    def replica_counts(self, block_ids):
        counts = {block: [] for block in block_ids}
        for node in self.live_data():
            try:
                inventory = self.inventory(node)
            except (OSError, ValueError):
                continue
            for block in inventory["blocks"]:
                if block["block_id"] in counts:
                    counts[block["block_id"]].append(node)
        return counts

    def client(self, user):
        os.environ["DFSHA_CA"] = str(self.secret_dir / "ca.crt")
        os.environ["DFSHA_SESSION"] = str(self.workdir / (user + "-session.json"))
        os.environ["DFSHA_INSECURE"] = "false"
        os.environ.pop("DFSHA_TOKEN", None)
        client = DfshaClient(",".join(self.controls.values()), quiet=True)
        eventually(lambda: client.login(user, self.credentials[user]), description=f"{user} login with leader")
        self.redactions.append(client.token)
        return client

    def sanitize(self, value):
        text = str(value).replace(str(self.workdir), "<WORKDIR>")
        for secret in self.redactions:
            text = text.replace(secret, "<REDACTED>")
        return text

    def saved_logs(self):
        result = {}
        for path in (self.workdir / "logs").glob("*.log"):
            lines = path.read_text(encoding="utf-8", errors="replace").splitlines()[-100:]
            result[path.name] = self.sanitize("\n".join(lines))
        return result


def run_suite(cluster, result, extended=False):
    client = cluster.client("demo")
    other = cluster.client("other")
    eventually(lambda: sum(bool(node.get("alive")) for node in client.status().get("nodes", [])) >= 3,
               description="three registered DataNodes before RF2 demonstration")
    checks = result["checks"]

    def check(name, action):
        print(f"[....] {name}", flush=True)
        start = time.perf_counter()
        try:
            value = action()
        except Exception as error:
            checks.append({"name": name, "status": "failed", "seconds": round(time.perf_counter() - start, 4),
                           "error": cluster.sanitize(error)})
            raise
        record = {"name": name, "status": "passed", "seconds": round(time.perf_counter() - start, 4)}
        if value is not None:
            record["evidence"] = value
        checks.append(record)
        print(f"[ OK ] {name} ({record['seconds']:.3f}s)", flush=True)
        return value

    def topology():
        status = client._control("GET", "/cluster/status")
        return status if all(node in json.dumps(status) for node in list(cluster.datas)[:3]) else None
    eventually(topology, description="three registered DataNodes")

    def auth_and_filesystem():
        expect_http(401, lambda: client._control("POST", "/auth/login", json_body={"username": "demo", "password": "deliberately-invalid"}))
        client.mkdir("/course")
        client.cd("/course")
        assert client.cwd == "/course"
        client.mkdir("empty")
        listing = client.ls(".")
        assert any(entry["name"] == "empty" for entry in listing["entries"])
        client.rmdir("empty")
        assert client.stat(".")["type"] == "dir"
        other.mkdir("/private")
        expect_http(404, lambda: client.stat("/private"))
        expect_http(404, lambda: other.stat("/course"))
        client.cd("/")
        return {"users": 2, "isolated_roots": True, "operations": ["login", "ls", "cd", "mkdir", "rmdir", "stat"]}
    check("autenticación y árbol privado por usuario", auth_and_filesystem)

    def empty_file():
        local = cluster.workdir / "empty.bin"
        local.write_bytes(b"")
        client.put(local, "/course/empty.bin")
        metadata = client.stat("/course/empty.bin")
        assert metadata["size"] == 0, f"unexpected empty-file size: {metadata}"
        blocks = metadata["blocks"] or []
        assert len(blocks) <= 1 and sum(block["size"] for block in blocks) == 0
        for block in blocks:
            assert block["block_id"] == hashlib.sha256(b"").hexdigest()
            assert len(set(block["stored_on"])) >= 2
        output = cluster.workdir / "empty-output.bin"
        client.get("/course/empty.bin", output)
        assert output.read_bytes() == b""
        client.rm("/course/empty.bin")
        return {"size": 0, "blocks": len(blocks), "sha256": hashlib.sha256(b"").hexdigest()}
    check("subida y bajada de un archivo vacío", empty_file)

    orphan = {}

    def orphan_upload():
        local = cluster.workdir / "orphan.bin"
        digest = payload(local, MIB, 269999)
        plan = client._control("POST", "/files/upload/plan", json_body={"path": "/course/aborted.bin", "size": MIB, "sha256": digest})
        stored = client._upload_block(local, plan["block_size"], plan["blocks"][0])
        client._control("POST", "/files/upload/abort", json_body={"upload_id": plan["upload_id"]})
        expect_http(404, lambda: client.stat("/course/aborted.bin"))
        orphan.update(block_id=stored["block_id"], nodes=stored["stored_on"])
        assert len(cluster.replica_counts([orphan["block_id"]])[orphan["block_id"]]) >= 2
        return {"block_id": orphan["block_id"], "uploaded_replicas": len(stored["stored_on"]), "published": False}
    check("una subida abortada no publica nada", orphan_upload)

    source = cluster.workdir / "source-24MiB.bin"
    source_hash = payload(source, 24 * MIB, 260024)
    remote = "/course/dataset.bin"

    def upload_distribution():
        client.put(source, remote, workers=4)
        metadata = client.stat(remote)
        blocks = metadata["blocks"]
        assert len(blocks) >= 3, f"file not partitioned: {len(blocks)} blocks"
        assert len({block["block_id"] for block in blocks}) == len(blocks), "test payload accidentally duplicates blocks"
        per_node = {}
        for block in blocks:
            assert len(set(block["stored_on"])) >= 2, f"missing replica for {block['index']}"
            for node in block["stored_on"]:
                per_node[node] = per_node.get(node, 0) + 1
        assert len(per_node) >= 3, f"file not distributed: {per_node}"
        assert max(per_node.values()) < len(blocks), f"one node contains entire file: {per_node}"
        output = cluster.workdir / "download.bin"
        client.get(remote, output, workers=4)
        assert file_sha256(output) == source_hash
        return {"size": source.stat().st_size, "sha256": source_hash, "block_count": len(blocks), "blocks_per_node": per_node, "replication_factor": 2}
    check("archivo en varios bloques, reparto, réplicas y SHA-256", upload_distribution)

    def known_hash_attack():
        block = client.stat(remote)["blocks"][0]
        source = block["stored_on"][0]
        # Se pone el bloque en todos los destinos posibles: así no basta con
        # comprobar que existe, hay que comprobar quién lo subió.
        for node in cluster.live_data():
            if node != source:
                cluster.http("POST", cluster.datas[source] + "/blocks/" + block["block_id"] + "/replicate",
                             data=json.dumps({"target": {"node": node, "url": cluster.datas[node]}}).encode(), internal=True)
        plan = other._control("POST", "/files/upload/plan", json_body={"path": "/forged.bin", "size": block["size"], "sha256": block["block_id"]})
        forged = {"index": 0, "block_id": block["block_id"], "size": block["size"], "stored_on": [t["node"] for t in plan["blocks"][0]["targets"]]}
        expect_http(409, lambda: other._control("POST", "/files/upload/commit", json_body={"upload_id": plan["upload_id"], "sha256": block["block_id"], "blocks": [forged]}))
        expect_http(404, lambda: other.stat("/forged.bin"))
        other._control("POST", "/files/upload/abort", json_body={"upload_id": plan["upload_id"]})
        return {"existing_hash_commit_status": 409, "other_account_file_visible": False}
    check("conocer el hash de un bloque ajeno no da acceso a él", known_hash_attack)

    def reader_writer():
        read_handle = client.open(remote, "r")["handle"]
        write_handle = client.lock(remote)["handle"]
        expect_http(409, lambda: client.open(remote, "w"))
        expect_http(409, lambda: client.rm(remote))
        offset, length = 8 * MIB - 17, 128
        original_slice = source.read_bytes()[offset:offset + length]
        range_file = cluster.workdir / "range.bin"
        client.read(remote, offset, length, output=range_file, handle=read_handle)
        assert range_file.read_bytes() == original_slice
        patch = cluster.workdir / "patch.bin"
        patch.write_bytes(b"DFSHA-COW-PATCH" * 8)
        expected = cluster.workdir / "expected.bin"
        shutil.copyfile(source, expected)
        with expected.open("r+b") as target:
            target.seek(offset)
            target.write(patch.read_bytes())
        client.write(write_handle, patch, offset=offset, workers=4)
        client.get(remote, range_file, workers=4)
        assert file_sha256(range_file) == file_sha256(expected)
        client.read(remote, offset, length, output=range_file, handle=read_handle)
        assert range_file.read_bytes() == original_slice, "read handle lost its immutable snapshot"
        client.close(write_handle)
        client.close(read_handle)
        return {"range_offset": offset, "range_length": length, "single_writer": True, "reader_snapshot": True, "copy_on_write_sha256": file_sha256(expected)}
    check("lectura por rango, open/close, lock y write con copia", reader_writer)
    expected = cluster.workdir / "expected.bin"
    expected_hash = file_sha256(expected)

    def incomplete_upload():
        plan = client._control("POST", "/files/upload/plan", json_body={"path": "/course/incomplete.bin", "size": source.stat().st_size, "sha256": source_hash})
        partial = client._upload_block(source, plan["block_size"], plan["blocks"][0])
        assert plan["n_blocks"] > 1
        expect_http(409, lambda: client._control("POST", "/files/upload/commit", json_body={"upload_id": plan["upload_id"], "blocks": [partial], "sha256": source_hash}))
        expect_http(404, lambda: client.stat("/course/incomplete.bin"))
        client._control("POST", "/files/upload/abort", json_body={"upload_id": plan["upload_id"]})
        expect_http(404, lambda: client.stat("/course/incomplete.bin"))
        block = client._download_plan(remote)["blocks"][0]
        target = block["targets"][0]
        expect_http(401, lambda: client._request("GET", client._block_url(target, block["block_id"])))
        signing_key = bytes.fromhex((cluster.secret_dir / "signing-key").read_text().strip())
        def signed_claims(claims):
            body = base64.urlsafe_b64encode(json.dumps(claims, separators=(",", ":")).encode()).rstrip(b"=")
            signature = base64.urlsafe_b64encode(hmac.new(signing_key, body, hashlib.sha256).digest()).rstrip(b"=")
            return body.decode() + "." + signature.decode()
        for node, expires in ((target["node"], int(time.time()) - 1), ("dn-wrong", int(time.time()) + 60)):
            token = signed_claims({"kind": "block-read", "user": "demo", "node": node, "block": block["block_id"], "exp": expires})
            expect_http(401, lambda token=token: client._request("GET", client._block_url(target, block["block_id"]), headers={"Authorization": "Bearer " + token}))
        expect_http(404, lambda: other._download_plan(remote))
        return {"partial_blocks_uploaded": 1, "planned_blocks": plan["n_blocks"], "incomplete_never_published": True, "session_token_denied_at_datanode": True, "expired_capability_denied": True, "wrong_node_capability_denied": True, "other_user_denied": True}
    check("publicación atómica y permisos de acceso a bloques", incomplete_upload)

    def benchmark():
        metrics = []
        for size in (MIB, 24 * MIB):
            local = cluster.workdir / f"benchmark-{size}.bin"
            digest = payload(local, size, seed=260000 + size)
            for workers in (1, 4):
                path = f"/course/bench-{size}-w{workers}.bin"
                before = time.perf_counter()
                client.put(local, path, workers=workers)
                upload = time.perf_counter() - before
                output = cluster.workdir / "benchmark-output.bin"
                before = time.perf_counter()
                client.get(path, output, workers=workers)
                download = time.perf_counter() - before
                assert file_sha256(output) == digest
                metrics.append({"bytes": size, "workers": workers, "upload_seconds": round(upload, 6), "download_seconds": round(download, 6), "upload_MiB_s": round(size / MIB / upload, 3), "download_MiB_s": round(size / MIB / download, 3), "sha256_verified": True})
        result["benchmarks"] = metrics
        return metrics
    check("medición de transferencias con 1 y 4 workers", benchmark)

    def corruption():
        block = client._download_plan(remote)["blocks"][0]
        target = block["targets"][0]
        node, block_id = target["node"], block["block_id"]
        path = cluster.workdir / node / block_id
        with path.open("r+b") as corrupt:
            corrupt.seek(-1, os.SEEK_END)
            last = corrupt.read(1)
            corrupt.seek(-1, os.SEEK_END)
            corrupt.write(bytes([last[0] ^ 1]))
            corrupt.flush()
            os.fsync(corrupt.fileno())
        expect_http(500, lambda: client._request("GET", client._block_url(target, block_id), headers=client._node_headers(target)))
        inventory = cluster.inventory(node)
        assert block_id not in {item["block_id"] for item in inventory["blocks"]}, "corruption advertised as valid"
        _, data, used_node = client._download_block(block)
        assert hashlib.sha256(data).hexdigest() == block_id
        assert used_node != node, "corrupt replica did not trigger fallback"
        counts = eventually(lambda: (counts if all(len(nodes) >= 2 for nodes in counts.values()) else None) if (counts := cluster.replica_counts([block_id])) else None,
                            description="automatic repair after corruption")
        return {"block_id": block_id, "corrupt_node": node, "fallback_node": used_node, "valid_replicas_after_repair": counts[block_id]}
    check("bloque corrupto: detección, otra réplica y reparación", corruption)

    def data_failure():
        plan = client._download_plan(remote)
        block = plan["blocks"][0]
        failed = block["targets"][0]["node"]
        cluster.stop(failed)
        _, data, used = client._download_block(block)
        assert hashlib.sha256(data).hexdigest() == block["block_id"] and used != failed
        output = cluster.workdir / "after-dn-loss.bin"
        client.get(remote, output, workers=4)
        assert file_sha256(output) == expected_hash
        ids = [item["block_id"] for item in plan["blocks"]]
        repaired = eventually(lambda: (counts if all(len(nodes) >= 2 for nodes in counts.values()) else None) if (counts := cluster.replica_counts(ids)) else None,
                              description="replicas restored after DataNode failure")
        cluster.start(failed)
        eventually(lambda: cluster.health(failed), description="restarted DataNode")
        return {"failed_node": failed, "fallback_node": used, "valid_replicas_during_outage": repaired, "restart": True}
    check("caída de un DataNode: lectura, reparación y regreso", data_failure)

    def leader_failure():
        client.stat(remote)
        old_url = client.control_url
        failed = next(node for node, url in cluster.controls.items() if url == old_url)
        cluster.stop(failed)
        metadata = eventually(lambda: client.stat(remote), timeout=45, description="new ControlNode leader")
        assert metadata["sha256"] == expected_hash
        new_url = client.control_url
        assert new_url != old_url
        client.mkdir("/after-leader-failover")
        output = cluster.workdir / "after-cn-loss.bin"
        client.get(remote, output)
        assert file_sha256(output) == expected_hash
        cluster.start(failed)
        eventually(lambda: cluster.health(failed), description="recovered ControlNode")
        return {"failed_node": failed, "new_leader": next(node for node, url in cluster.controls.items() if url == new_url), "confirmed_metadata_preserved": True}
    check("caída del líder: elección, metadatos conservados y regreso", leader_failure)

    def scale_out():
        cluster.start("dn-04")
        eventually(lambda: cluster.health("dn-04"), description="fourth DataNode")
        eventually(lambda: "dn-04" in json.dumps(client._control("GET", "/cluster/status")), description="fourth node registration")
        inventory = eventually(lambda: (value if value["blocks"] else None) if (value := cluster.inventory("dn-04")) else None,
                               timeout=180, description="automatic rebalancing to new DataNode")
        return {"node": "dn-04", "blocks_received": len(inventory["blocks"]), "used_bytes": inventory["used"], "capacity_bytes": inventory["capacity"]}
    check("cuarto DataNode en caliente y rebalanceo", scale_out)

    def metadata_restart():
        data_nodes = cluster.live_data()
        for node in data_nodes:
            cluster.stop(node)
        for node in cluster.controls:
            cluster.stop(node)
        for node in cluster.controls:
            cluster.start(node)
        for node in data_nodes:
            cluster.start(node)
        metadata = eventually(lambda: client.stat(remote), timeout=45, description="persistent metadata after complete cluster restart")
        assert metadata["sha256"] == expected_hash
        assert client.stat("/after-leader-failover")["type"] == "dir"
        output = cluster.workdir / "after-restart.bin"
        eventually(lambda: client.get(remote, output), timeout=45, description="persistent blocks after complete cluster restart")
        assert file_sha256(output) == expected_hash
        return {"all_controlnodes_restarted": 3, "all_datanodes_restarted": len(data_nodes), "sha256_verified": True, "new_directory_preserved": True}
    check("reinicio completo del clúster sin perder datos", metadata_restart)

    def no_quorum():
        client.stat(remote)
        sole = next(node for node, url in cluster.controls.items() if url == client.control_url)
        stopped = [node for node in cluster.controls if node != sole]
        for node in stopped:
            cluster.stop(node)
        time.sleep(2)
        def rejected():
            try:
                client._request("POST", cluster.controls[sole] + "/fs/mkdir", json_body={"path": "/must-not-commit"}, timeout=8)
            except DfshaHttpError as error:
                assert error.status == 503, f"unexpected minority response: {error}"
                return True
            except NETWORK_ERRORS:
                # Mientras el líder deja de serlo una conexión se puede cerrar;
                # se insiste hasta ver un 503. Un 2xx es siempre un fallo.
                return False
            raise AssertionError("minority acknowledged a write without quorum")
        eventually(rejected, timeout=20, description="explicit HTTP 503 from minority")
        for node in stopped:
            cluster.start(node)
        eventually(lambda: client.stat(remote), timeout=45, description="majority recovery")
        expect_http(404, lambda: client.stat("/must-not-commit"))
        return {"remaining_controlnodes": 1, "write_status": 503, "uncommitted_path_absent_after_recovery": True}
    check("sin mayoría no se acepta ninguna escritura", no_quorum)

    def rename_remove():
        client.mv(remote, "/course/moved.bin")
        expect_http(404, lambda: client.stat(remote))
        assert client.stat("/course/moved.bin")["sha256"] == expected_hash
        client.rm("/course/moved.bin")
        expect_http(404, lambda: client.stat("/course/moved.bin"))
        return {"mv": True, "rm": True}
    check("mover y borrar un archivo publicado", rename_remove)

    if extended:
        def renewal_and_expiry():
            local = cluster.workdir / "renewal.bin"
            digest = payload(local, MIB, 266060)
            original_upload, original_control = client._upload_block, client._control
            renewals = []
            def slow_upload(*args, **kwargs):
                # El cliente real renueva la reserva cada 20 s.
                time.sleep(21)
                return original_upload(*args, **kwargs)
            def tracked_control(method, endpoint, *args, **kwargs):
                value = original_control(method, endpoint, *args, **kwargs)
                if endpoint == "/files/upload/renew":
                    renewals.append(value["expires_at"])
                return value
            client._upload_block, client._control = slow_upload, tracked_control
            try:
                client.put(local, "/course/renewed.bin", workers=1)
            finally:
                client._upload_block, client._control = original_upload, original_control
            assert renewals, "client failed to renew an upload lasting over 20s"
            assert client.stat("/course/renewed.bin")["sha256"] == digest
            plan = client._control("POST", "/files/upload/plan", json_body={"path": "/course/expired.bin", "size": MIB, "sha256": digest})
            deadline = datetime.fromisoformat(plan["expires_at"].replace("Z", "+00:00")).timestamp() + 1
            while time.time() < deadline:
                remaining = deadline - time.time()
                print(f"[WAIT] real upload reservation expiry: {remaining:.0f}s", flush=True)
                time.sleep(min(15, max(0, remaining)))
            expect_http(410, lambda: client._control("POST", "/files/upload/commit", json_body={"upload_id": plan["upload_id"], "blocks": [], "sha256": digest}))
            expect_http(404, lambda: client.stat("/course/expired.bin"))
            target = plan["blocks"][0]["targets"][0]
            # Una petición vacía basta para ver el código de autorización.
            expect_http(401, lambda: client._request("PUT", client._block_url(target, digest), data=b"", headers=client._node_headers(target)))
            return {"client_renewals": len(renewals), "client_renew_interval_seconds": 20, "expired_reservation_status": 410, "expired_write_capability_status": 401, "file_published_after_expiry": False}
        check("renovación automática y vencimiento de la reserva", renewal_and_expiry)

        def reclamation():
            benchmark_path = f"/course/bench-{24 * MIB}-w4.bin"
            blocks = client.stat(benchmark_path)["blocks"]
            ids = [block["block_id"] for block in blocks]
            last_progress = [0.0]
            def reclaimed():
                orphan_copies = cluster.replica_counts([orphan["block_id"]])[orphan["block_id"]]
                counts = cluster.replica_counts(ids)
                if time.monotonic() - last_progress[0] > 15:
                    print(f"[WAIT] orphan GC and replica retirement: orphan_copies={len(orphan_copies)}, replica_counts={[len(nodes) for nodes in counts.values()]}", flush=True)
                    last_progress[0] = time.monotonic()
                if not orphan_copies and all(len(nodes) == 2 for nodes in counts.values()):
                    return counts
                return None
            counts = eventually(reclaimed, timeout=300, description="real GC grace and safe excess-replica retirement")
            output = cluster.workdir / "after-gc.bin"
            client.get(benchmark_path, output)
            assert file_sha256(output) == client.stat(benchmark_path)["sha256"]
            return {"orphan_block": orphan["block_id"], "orphan_copies_after_gc": 0, "valid_replica_counts": {block: len(nodes) for block, nodes in counts.items()}, "file_sha256_verified_after_gc": True, "gc_grace_seconds": 120}
        check("borrado de huérfanos y de copias sobrantes", reclamation)
    result["storage"] = {node: {key: value for key, value in cluster.inventory(node).items() if key in ("node_id", "used", "capacity")} for node in cluster.live_data()}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--bin-dir", type=Path, default=ROOT / "bin")
    parser.add_argument("--output", type=Path, default=ROOT / "results" / "integration.json")
    parser.add_argument("--keep-workdir", action="store_true", help="conservar la carpeta temporal para depurar")
    parser.add_argument("--extended", action="store_true", help="incluir los escenarios lentos: renovación (20 s), vencimiento (60 s) y borrado (120 s)")
    args = parser.parse_args()
    workdir = Path(tempfile.mkdtemp(prefix="dfsha-integration-"))
    cluster = NativeCluster(args.bin_dir.resolve(), workdir)
    result = {"suite": "native-process-integration", "status": "running", "tls_verified": True,
              "topology": {"controlnodes": 3, "datanodes_initial": 3, "datanodes_after_scaleout": 4},
              "checks": [], "benchmarks": [], "extended": args.extended, "started_utc": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())}
    started = time.perf_counter()
    original_env = {key: os.environ.get(key) for key in ("DFSHA_CA", "DFSHA_SESSION", "DFSHA_INSECURE", "DFSHA_TOKEN")}
    try:
        cluster.initialize()
        run_suite(cluster, result, extended=args.extended)
        result["status"] = "passed"
    except Exception as error:
        result["status"] = "failed"
        result["error"] = cluster.sanitize(f"{type(error).__name__}: {error}")
        result["traceback"] = cluster.sanitize(traceback.format_exc())
        print(result["error"], file=sys.stderr)
    finally:
        cluster.close()
        result["total_seconds"] = round(time.perf_counter() - started, 4)
        result["logs"] = cluster.saved_logs()
        result["all_subprocesses_stopped"] = all(process.poll() is not None for process in cluster.processes.values())
        args.output.parent.mkdir(parents=True, exist_ok=True)
        args.output.write_text(json.dumps(result, indent=2, ensure_ascii=False) + "\n", encoding="utf-8")
        for key, value in original_env.items():
            if value is None:
                os.environ.pop(key, None)
            else:
                os.environ[key] = value
        if args.keep_workdir:
            print(f"Carpeta temporal conservada: {workdir}", file=sys.stderr)
        else:
            shutil.rmtree(workdir)
    status = "aprobada" if result["status"] == "passed" else "fallida"
    print(f"Integración {status}: {len(result['checks'])} escenarios; resultado en {args.output}", flush=True)
    return 0 if result["status"] == "passed" else 1


if __name__ == "__main__":
    sys.exit(main())
