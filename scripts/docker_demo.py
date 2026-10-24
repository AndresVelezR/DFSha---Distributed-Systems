#!/usr/bin/env python3
"""Se ejecuta dentro de client; conserva archivos y métricas sin secretos."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import shutil
import sys
import time

sys.path.insert(0, str(Path(__file__).resolve().parent.parent / "client"))
from dfsha import DfshaClient, file_sha256


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--size-mib", type=int, default=24)
    args = parser.parse_args()
    if not 1 <= args.size_mib <= 512:
        parser.error("tamaño de prueba fuera de rango")
    report = {"status": "running", "checks": [], "metrics": {}}
    root = Path(__file__).resolve().parent.parent
    folder = root / "demo-files"
    folder.mkdir(exist_ok=True)
    run = str(time.time_ns())
    remote = "/demo-" + run
    source = folder / (run + "-original.bin")
    copied = folder / (run + "-download.bin")
    result_file = folder / (run + "-results.json")
    client = DfshaClient(quiet=True)

    def check(name, condition=True, details=None):
        if not condition:
            raise RuntimeError("Prueba fallida: " + name)
        report["checks"].append({"name": name, "passed": True, "details": details})
        print("PASS " + name, flush=True)

    try:
        with open("/secrets/credentials.txt", encoding="utf-8") as secret:
            username, password = secret.readline().strip().split(maxsplit=1)
        client.login(username, password)
        password = None
        check("autenticación con credenciales generadas")

        deadline = time.monotonic() + 60
        while True:
            topology = client.status()
            if sum(bool(node.get("alive")) for node in topology.get("nodes", [])) >= 3:
                break
            if time.monotonic() >= deadline:
                raise RuntimeError("se requieren tres DataNodes registrados para demostrar RF 2")
            time.sleep(.5)
        client.mkdir(remote)
        client.cd(remote)
        check("mkdir y cd", client.cwd == remote)
        with open(source, "wb") as output:
            for _ in range(args.size_mib):
                output.write(os.urandom(1024 * 1024))
        start = time.perf_counter()
        client.put(source, "original.bin")
        report["metrics"]["put_seconds"] = time.perf_counter() - start
        start = time.perf_counter()
        client.get("original.bin", copied)
        report["metrics"]["get_seconds"] = time.perf_counter() - start
        digest = file_sha256(source)
        check("put/get y SHA-256", file_sha256(copied) == digest, {"bytes": source.stat().st_size, "sha256": digest})
        metadata = client.stat("original.bin")
        blocks = metadata["blocks"]
        check("múltiples bloques para archivo grande", len(blocks) > 1 if args.size_mib > 8 else len(blocks) >= 1,
              {"blocks": len(blocks), "block_size": metadata["block_size"],
               "replicas": [{"index": block["index"], "stored_on": block["stored_on"]} for block in blocks]})
        check("réplicas en nodos diferentes", all(len(set(block["stored_on"])) >= 2 for block in blocks))
        offset = metadata["block_size"] - 3 if source.stat().st_size > metadata["block_size"] else 0
        ranged = folder / (run + "-range.bin")
        opened = client.open("original.bin", "r")
        client.read("original.bin", offset, 11, output=ranged, handle=opened["handle"])
        client.close(opened["handle"])
        with open(source, "rb") as original:
            original.seek(offset)
            expected_range = original.read(11)
        check("open/read rango/close", ranged.read_bytes() == expected_range)
        patch = folder / (run + "-patch.bin")
        patch.write_bytes(b"DFSha COW")
        writer = client.lock("original.bin")
        client.write(writer["handle"], patch, offset=10)
        client.close(writer["handle"])
        expected = folder / (run + "-expected.bin")
        shutil.copyfile(source, expected)
        with open(expected, "r+b") as target:
            target.seek(10)
            target.write(patch.read_bytes())
        client.get("original.bin", copied)
        check("lock/write copy-on-write/close", file_sha256(copied) == file_sha256(expected))
        empty = folder / (run + "-empty.bin")
        empty.write_bytes(b"")
        empty_copy = folder / (run + "-empty-copy.bin")
        client.put(empty, "empty.bin")
        client.get("empty.bin", empty_copy)
        check("archivo vacío roundtrip", empty_copy.stat().st_size == 0 and file_sha256(empty_copy) == hashlib.sha256(b"").hexdigest())
        client.mv("empty.bin", "renamed.bin")
        client.rm("renamed.bin")
        client.rm("original.bin")
        client.cd("/")
        client.rmdir(remote)
        check("mv/rm/rmdir")
        status = client._control("GET", "/cluster/status")
        report["topology"] = {"nodes": [{"node_id": node.get("node_id"), "used": node.get("used")} for node in status.get("nodes", [])]}
        report["status"] = "passed"
        print("Métricas reales: " + json.dumps(report["metrics"], ensure_ascii=False), flush=True)
        print("Resultados: " + str(result_file), flush=True)
        return 0
    except Exception as error:
        report["status"] = "failed"
        # No serializar sesiones ni respuestas que contengan capacidades.
        report["error"] = type(error).__name__ + ": " + str(error)
        print("DEMO FALLÓ: " + str(error), file=sys.stderr)
        return 1
    finally:
        result_file.write_text(json.dumps(report, indent=2, ensure_ascii=False), encoding="utf-8")


if __name__ == "__main__":
    sys.exit(main())
