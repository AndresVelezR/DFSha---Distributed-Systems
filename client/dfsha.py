#!/usr/bin/env python3
"""Cliente CLI de DFSha.

Pide al ControlNode los planes de lectura y escritura, y mueve los bloques
directamente contra los DataNodes. Nunca tiene direcciones de DataNodes en su
configuración: las recibe en cada plan.
"""
import argparse
import hashlib
import http.client
import json
import os
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
from concurrent.futures import ThreadPoolExecutor, as_completed

TOKEN_FILE = os.path.expanduser("~/.dfsha_token")
DEFAULT_CONTROL_URL = "http://localhost:8000"
DEFAULT_WORKERS = 4
BLOCK_TIMEOUT_SECONDS = 300

# Espera antes de cada reintento de una escritura de bloque [Hito 1, cap. 3].
RETRY_WAITS_SECONDS = (1, 2, 4)
# Fallos de red: no hubo respuesta del servidor, así que reintentar es seguro.
NETWORK_ERRORS = (urllib.error.URLError, ConnectionError, TimeoutError, http.client.HTTPException)


class DfshaHttpError(RuntimeError):
    """El servidor respondió con un código de error HTTP."""

    def __init__(self, status, body):
        super().__init__(f"HTTP {status}: {body}")
        self.status = status


def with_network_retries(action, description):
    """Ejecuta action y, si falla por red, la reintenta hasta 3 veces (1s, 2s, 4s).

    Es seguro porque subir un bloque es idempotente: su id es la firma de su
    contenido. Una respuesta de error del servidor (DfshaHttpError) no es un
    fallo de red y se propaga sin reintentar.
    """
    for attempt, wait in enumerate(RETRY_WAITS_SECONDS, start=1):
        try:
            return action()
        except DfshaHttpError:
            raise
        except NETWORK_ERRORS as error:
            print(f"  fallo de red en {description} ({error}); reintento {attempt}/{len(RETRY_WAITS_SECONDS)} en {wait}s", file=sys.stderr)
            time.sleep(wait)
    return action()


class DfshaClient:
    def __init__(self, control_url):
        self.control_url = control_url.rstrip("/")
        self.token = os.environ.get("DFSHA_TOKEN") or self._read_saved_token()

    # --- transporte HTTP -------------------------------------------------

    @staticmethod
    def _read_saved_token():
        try:
            with open(TOKEN_FILE, encoding="utf-8") as f:
                return f.read().strip()
        except OSError:
            return ""

    def _request(self, method, url, data=None, json_body=None, headers=None, timeout=120):
        headers = dict(headers or {})
        if self.token:
            headers.setdefault("Authorization", "Bearer " + self.token)
        if json_body is not None:
            data = json.dumps(json_body).encode()
            headers["Content-Type"] = "application/json"
        request = urllib.request.Request(url, data=data, method=method, headers=headers)
        try:
            with urllib.request.urlopen(request, timeout=timeout) as response:
                body = response.read()
                if "application/json" in response.headers.get("Content-Type", ""):
                    return json.loads(body or b"{}")
                return body
        except urllib.error.HTTPError as error:
            raise DfshaHttpError(error.code, error.read().decode(errors="replace")) from error

    def _control(self, method, endpoint, query=None, json_body=None):
        url = self.control_url + endpoint
        if query:
            url += "?" + urllib.parse.urlencode(query)
        return self._request(method, url, json_body=json_body)

    @staticmethod
    def _block_url(node, block_id):
        return f"http://{node['host']}:{node['port']}/blocks/{block_id}"

    # --- RF1: gestión del sistema de archivos ----------------------------

    def login(self, username, password):
        response = self._control("POST", "/auth/login", json_body={"username": username, "password": password})
        self.token = response["token"]
        with open(TOKEN_FILE, "w", encoding="utf-8") as f:
            f.write(self.token)
        print("Autenticación correcta; token guardado en", TOKEN_FILE)

    def ls(self, path):
        print_json(self._control("GET", "/fs/ls", query={"path": path}))

    def mkdir(self, path):
        print_json(self._control("POST", "/fs/mkdir", json_body={"path": path}))

    def rmdir(self, path):
        self._control("DELETE", "/fs/rmdir", query={"path": path})
        print("Directorio borrado:", path)

    def rm(self, path):
        self._control("DELETE", "/fs/rm", query={"path": path})
        print("Borrado de metadatos:", path)

    def mv(self, src, dst):
        print_json(self._control("POST", "/fs/mv", json_body={"src": src, "dst": dst}))

    def stat(self, path):
        print_json(self._control("GET", "/fs/stat", query={"path": path}))

    # --- RF2: transferencia de archivos -----------------------------------

    def put(self, local_path, remote_path, workers=DEFAULT_WORKERS):
        size = os.path.getsize(local_path)
        plan = self._control("POST", "/files/upload/plan", json_body={"path": remote_path, "size": size})
        block_size = plan["block_size"]
        print(f"Plan: {plan['n_blocks']} bloques de hasta {block_size} bytes")

        stored_blocks = []
        try:
            with ThreadPoolExecutor(max_workers=workers) as pool:
                futures = [pool.submit(self._upload_block, local_path, block_size, block_plan) for block_plan in plan["blocks"]]
                for future in as_completed(futures):
                    block = future.result()
                    stored_blocks.append(block)
                    print(f"  bloque {block['index']} -> {', '.join(block['stored_on'])}")
        except Exception:
            try:
                self._control("POST", "/files/upload/abort", json_body={"upload_id": plan["upload_id"]})
            finally:
                raise

        stored_blocks.sort(key=lambda block: block["index"])
        # TODO Hito 3: si el commit responde 410 (reserva vencida), pedir un plan nuevo.
        commit = self._control("POST", "/files/upload/commit", json_body={"upload_id": plan["upload_id"], "blocks": stored_blocks})
        print_json(commit)

    def _upload_block(self, local_path, block_size, block_plan):
        """Lee su bloque del archivo local y lo sube al primer destino, que lo
        propaga a los demás.

        Cada worker lee solo su bloque, así que en memoria nunca hay más de
        workers x block_size bytes, sin importar el tamaño del archivo.
        Ante un fallo de red reintenta contra el mismo destino (ver with_network_retries).
        """
        data = read_block(local_path, block_plan["index"], block_size)
        block_id = hashlib.sha256(data).hexdigest()
        first, *others = block_plan["targets"]
        headers = {"Content-Type": "application/octet-stream"}
        if others:
            headers["X-Forward-To"] = ",".join(f"http://{t['host']}:{t['port']}" for t in others)
            for position, target in enumerate(others, start=1):
                headers[f"X-Forward-Node-{position}"] = target["node"]
        response = with_network_retries(
            lambda: self._request("PUT", self._block_url(first, block_id), data=data, headers=headers, timeout=BLOCK_TIMEOUT_SECONDS),
            f"bloque {block_plan['index']} -> {first['node']}",
        )
        stored_on = response.get("stored_on", [t["node"] for t in block_plan["targets"]])
        return {"index": block_plan["index"], "block_id": block_id, "size": len(data), "stored_on": stored_on}

    def get(self, remote_path, local_path, workers=DEFAULT_WORKERS):
        """Descarga los bloques en paralelo y escribe cada uno en su posición.

        Se escribe sobre un archivo .part que solo se renombra al nombre final
        cuando todos los bloques llegaron y pasaron la verificación de firma.
        """
        plan = self._control("GET", "/files/download/plan", query={"path": remote_path})
        partial_path = local_path + ".part"
        with open(partial_path, "wb") as f:
            f.truncate(plan["size"])
        try:
            with ThreadPoolExecutor(max_workers=workers) as pool:
                futures = [pool.submit(self._download_block_into, partial_path, plan["block_size"], block) for block in plan["blocks"]]
                for future in as_completed(futures):
                    index, node = future.result()
                    print(f"  bloque {index} <- {node}")
        except Exception:
            os.remove(partial_path)
            raise
        os.replace(partial_path, local_path)
        print(f"Archivo reconstruido en {local_path} ({os.path.getsize(local_path)} bytes)")

    def _download_block_into(self, partial_path, block_size, block):
        """Descarga un bloque y lo escribe en su posición dentro de partial_path."""
        index, data, node = self._download_block(block)
        write_block(partial_path, index, block_size, data)
        return index, node

    def _download_block(self, block):
        """Descarga el bloque de la primera réplica que responda con la firma correcta.

        Según el Hito 1, en lectura no se reintenta la misma réplica: si una no
        responde, se pasa de inmediato a la siguiente.
        """
        last_error = None
        for replica in block["replicas"]:
            try:
                data = self._request("GET", self._block_url(replica, block["block_id"]), timeout=BLOCK_TIMEOUT_SECONDS)
                if hashlib.sha256(data).hexdigest() != block["block_id"]:
                    raise RuntimeError("hash inválido")
                return block["index"], data, replica["node"]
            except Exception as error:
                last_error = error
        raise last_error or RuntimeError("sin réplicas")


def read_block(path, index, block_size):
    """Lee el bloque número index (desde 0) de un archivo local."""
    with open(path, "rb") as f:
        f.seek(index * block_size)
        return f.read(block_size)


def write_block(path, index, block_size, data):
    """Escribe data como el bloque número index de un archivo ya creado."""
    with open(path, "r+b") as f:
        f.seek(index * block_size)
        f.write(data)


def print_json(value):
    print(json.dumps(value, indent=2, ensure_ascii=False))


def build_parser():
    parser = argparse.ArgumentParser(description="Cliente CLI de DFSha")
    parser.add_argument("--control", default=os.environ.get("DFSHA_CONTROL", DEFAULT_CONTROL_URL))
    commands = parser.add_subparsers(dest="cmd", required=True)

    login = commands.add_parser("login")
    login.add_argument("username")
    login.add_argument("password")

    commands.add_parser("ls").add_argument("path", nargs="?", default="/")
    commands.add_parser("mkdir").add_argument("path")
    commands.add_parser("rmdir").add_argument("path")
    commands.add_parser("rm").add_argument("path")
    commands.add_parser("stat").add_argument("path")

    mv = commands.add_parser("mv")
    mv.add_argument("src")
    mv.add_argument("dst")

    put = commands.add_parser("put")
    put.add_argument("local")
    put.add_argument("remote")
    put.add_argument("--workers", type=int, default=DEFAULT_WORKERS)

    get = commands.add_parser("get")
    get.add_argument("remote")
    get.add_argument("local")
    get.add_argument("--workers", type=int, default=DEFAULT_WORKERS)
    return parser


def main():
    args = build_parser().parse_args()
    client = DfshaClient(args.control)
    if args.cmd == "login":
        client.login(args.username, args.password)
        return
    if not client.token:
        sys.exit("Primero ejecuta: dfsha.py login demo demo")

    if args.cmd in ("ls", "mkdir", "rmdir", "rm", "stat"):
        getattr(client, args.cmd)(args.path)
    elif args.cmd == "mv":
        client.mv(args.src, args.dst)
    elif args.cmd == "put":
        client.put(args.local, args.remote, args.workers)
    elif args.cmd == "get":
        client.get(args.remote, args.local, args.workers)


if __name__ == "__main__":
    main()
