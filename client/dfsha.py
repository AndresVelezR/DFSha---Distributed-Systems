#!/usr/bin/env python3
"""Cliente de DFSha: línea de comandos y API en Python, sin dependencias.

El cliente le pide al ControlNode un plan (qué bloques y en qué DataNodes) y
mueve los bloques directamente con los DataNodes, varios a la vez. Cada worker
tiene en memoria como máximo un bloque (64 MiB). Una descarga solo reemplaza
el archivo local después de verificar el SHA-256 del archivo completo.
"""
import argparse
import getpass
import hashlib
import http.client
import json
import os
import posixpath
import ssl
import sys
import tempfile
import threading
import time
import urllib.error
import urllib.parse
import urllib.request
from concurrent.futures import ThreadPoolExecutor, as_completed

DEFAULT_CONTROL_URL = "https://localhost:8000"
DEFAULT_WORKERS = 4
# Los tiempos límite de urllib aplican a cada operación de red, no al total:
# una transferencia lenta pero que avanza no se corta.
BLOCK_TIMEOUT_SECONDS = 60
CONTROL_TIMEOUT_SECONDS = 15
MAX_BLOCK_SIZE = 64 * 1024 * 1024
UPLOAD_RENEW_SECONDS = 20
READ_REFRESH_SECONDS = 40
RETRY_WAITS_SECONDS = (1, 2, 4)
NETWORK_ERRORS = (urllib.error.URLError, ConnectionError, TimeoutError, http.client.HTTPException)


class DfshaHttpError(RuntimeError):
    """Error HTTP; conserva el cuerpo para descubrir el nuevo líder."""
    def __init__(self, status, body):
        self.status, self.body = status, body
        try:
            self.details = json.loads(body) if isinstance(body, str) else body
        except (ValueError, TypeError):
            self.details = {}
        if not isinstance(self.details, dict):
            self.details = {}
        super().__init__(f"HTTP {status}: {body}")


def with_network_retries(action, description):
    """Reintenta una escritura idempotente de bloque, con esperas 1s, 2s y 4s."""
    for attempt, wait in enumerate(RETRY_WAITS_SECONDS, start=1):
        try:
            return action()
        except DfshaHttpError:
            raise
        except NETWORK_ERRORS as error:
            print(f"  fallo de red en {description} ({error}); reintento {attempt}/3 en {wait}s", file=sys.stderr)
            time.sleep(wait)
    return action()


def file_sha256(path):
    digest = hashlib.sha256()
    with open(path, "rb") as source:
        for chunk in iter(lambda: source.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def read_block(path, index, block_size):
    with open(path, "rb") as source:
        source.seek(index * block_size)
        return source.read(block_size)


def write_block(path, index, block_size, data):
    # Cada worker usa un descriptor independiente y no comparte offset.
    with open(path, "r+b") as target:
        target.seek(index * block_size)
        target.write(data)


def print_json(value):
    print(json.dumps(value, indent=2, ensure_ascii=False))


def positive_workers(value):
    value = int(value)
    if not 1 <= value <= 64:
        raise argparse.ArgumentTypeError("workers debe estar entre 1 y 64")
    return value


class DfshaClient:
    def __init__(self, control_url=None, quiet=False):
        self.quiet = quiet
        self.session_file = os.path.abspath(os.path.expanduser(os.environ.get("DFSHA_SESSION", "~/.dfsha/session.json")))
        self.session = self._read_session()
        configured = control_url or os.environ.get("DFSHA_CONTROLS") or os.environ.get("DFSHA_CONTROL")
        configured = configured or self.session.get("controls") or DEFAULT_CONTROL_URL
        if isinstance(configured, str):
            configured = configured.split(",")
        self.controls = list(dict.fromkeys(url.strip().rstrip("/") for url in configured if url.strip()))
        if not self.controls:
            raise ValueError("se requiere al menos un ControlNode")
        for url in self.controls:
            self._validate_url(url)
        self.control_url = self.controls[0]
        self.token = os.environ.get("DFSHA_TOKEN") or self.session.get("token", "")
        self.cwd = self.session.get("cwd", "/")
        self.handles = self.session.get("handles", {})
        ca_file = os.environ.get("DFSHA_CA")
        if not ca_file and os.path.isfile(".secrets/ca.crt"):
            ca_file = ".secrets/ca.crt"
        self.ssl_context = (ssl._create_unverified_context() if os.environ.get("DFSHA_INSECURE", "").lower() == "true"
                            else ssl.create_default_context(cafile=ca_file or None))

    @staticmethod
    def _validate_url(url):
        parsed = urllib.parse.urlsplit(url)
        if parsed.scheme not in ("http", "https") or not parsed.netloc or parsed.username or parsed.password:
            raise ValueError("URL de nodo inválida; usa http(s)://host:puerto sin credenciales")

    def _read_session(self):
        try:
            with open(self.session_file, encoding="utf-8") as source:
                state = json.load(source)
            return state if isinstance(state, dict) else {}
        except (OSError, ValueError):
            return {}

    def _save_session(self):
        folder = os.path.dirname(self.session_file)
        os.makedirs(folder, mode=0o700, exist_ok=True)
        state = dict(self.session, token=self.token, controls=self.controls, cwd=self.cwd, handles=self.handles)
        descriptor, partial = tempfile.mkstemp(prefix=".session-", dir=folder)
        try:
            os.chmod(partial, 0o600)
            with os.fdopen(descriptor, "w", encoding="utf-8") as target:
                json.dump(state, target, ensure_ascii=False)
                target.flush()
                os.fsync(target.fileno())
            os.replace(partial, self.session_file)
            os.chmod(self.session_file, 0o600)
            self.session = state
        finally:
            if os.path.exists(partial):
                os.unlink(partial)

    def _print(self, value):
        if not self.quiet:
            print_json(value)

    def _progress(self, message):
        if not self.quiet:
            print(message, file=sys.stderr)

    def path(self, path):
        if not isinstance(path, str) or "\x00" in path or "\\" in path:
            raise ValueError("ruta DFS inválida: usa separadores / y evita bytes nulos")
        return posixpath.normpath("/" + posixpath.join(self.cwd, path).lstrip("/"))

    # Transporte: TLS verificado y capacidades temporales para los DataNodes.
    def _request(self, method, url, data=None, json_body=None, headers=None, timeout=120):
        self._validate_url(url)
        if url.startswith("http://") and os.environ.get("DFSHA_INSECURE", "").lower() != "true":
            raise ValueError("HTTPS obligatorio; HTTP solo para pruebas con DFSHA_INSECURE=true")
        headers = dict(headers or {})
        if self.token:
            headers.setdefault("Authorization", "Bearer " + self.token)
        if json_body is not None:
            data = json.dumps(json_body).encode()
            headers["Content-Type"] = "application/json"
        request = urllib.request.Request(url, data=data, method=method, headers=headers)
        try:
            with urllib.request.urlopen(request, timeout=timeout, context=self.ssl_context) as response:
                body = response.read()
                if response.status == 204:
                    return {}
                if "application/json" in response.headers.get("Content-Type", ""):
                    return json.loads(body or b"{}")
                return body
        except urllib.error.HTTPError as error:
            raise DfshaHttpError(error.code, error.read().decode(errors="replace")) from error

    def _control(self, method, endpoint, query=None, json_body=None, timeout=CONTROL_TIMEOUT_SECONDS):
        suffix = endpoint + ("?" + urllib.parse.urlencode(query) if query else "")
        last_error = None
        # Durante una elección no hay líder por unos segundos: se espera
        # 1, 2 y 4 s entre rondas antes de rendirse.
        for wait in (0,) + RETRY_WAITS_SECONDS:
            if wait:
                time.sleep(wait)
            candidates = list(dict.fromkeys([self.control_url] + self.controls))
            visited = set()
            while candidates:
                control = candidates.pop(0)
                if control in visited:
                    continue
                visited.add(control)
                try:
                    response = self._request(method, control + suffix, json_body=json_body, timeout=timeout)
                    self.control_url = control
                    if control not in self.controls:
                        self.controls.append(control)
                    return response
                except DfshaHttpError as error:
                    if error.status not in (502, 503, 504):
                        raise
                    last_error = error
                    leader = error.details.get("leader")
                    if isinstance(leader, str) and leader:
                        try:
                            self._validate_url(leader)
                        except ValueError:
                            continue
                        candidates.insert(0, leader.rstrip("/"))
                except NETWORK_ERRORS as error:
                    last_error = error
        raise last_error or RuntimeError("ningún ControlNode disponible")

    @staticmethod
    def _block_url(node, block_id):
        base = node["url"].rstrip("/")
        return base if base.endswith("/blocks/" + block_id) else base + "/blocks/" + block_id

    @staticmethod
    def _node_headers(node):
        # Nunca enviar el token de sesión del usuario a un DataNode.
        return {"Authorization": "Bearer " + node["token"]}

    # RF1: espacio de nombres y sesión persistente.
    def login(self, username, password=None):
        if password is None:
            password = getpass.getpass("Contraseña DFSha: ")
        response = self._control("POST", "/auth/login", json_body={"username": username, "password": password})
        self.token = response["token"]
        if self.session.get("username") != username:
            self.cwd, self.handles = "/", {}
        self.session["username"] = username
        self._save_session()
        self._progress("Autenticación correcta; sesión guardada en " + self.session_file)
        return {"username": username, "session": self.session_file}

    def ls(self, path="."):
        result = self._control("GET", "/fs/ls", query={"path": self.path(path)})
        self._print(result)
        return result

    def mkdir(self, path):
        result = self._control("POST", "/fs/mkdir", json_body={"path": self.path(path)})
        self._print(result)
        return result

    def rmdir(self, path):
        result = self._control("DELETE", "/fs/rmdir", query={"path": self.path(path)})
        self._print(result)
        return result

    def rm(self, path):
        result = self._control("DELETE", "/fs/rm", query={"path": self.path(path)})
        self._print(result)
        return result

    def mv(self, src, dst):
        result = self._control("POST", "/fs/mv", json_body={"src": self.path(src), "dst": self.path(dst)})
        self._print(result)
        return result

    def stat(self, path):
        result = self._control("GET", "/fs/stat", query={"path": self.path(path)})
        self._print(result)
        return result

    def cd(self, path="/"):
        absolute = self.path(path)
        result = self._control("GET", "/fs/stat", query={"path": absolute})
        if result.get("type") not in ("dir", "directory") and not result.get("is_dir"):
            raise ValueError("cd requiere un directorio")
        self.cwd = absolute
        self._save_session()
        self._print({"cwd": self.cwd})
        return self.cwd

    def status(self):
        result = self._control("GET", "/cluster/status")
        self._print(result)
        return result

    # RF2: transferencias concurrentes acotadas a workers * block_size.
    def put(self, local_path, remote_path, workers=DEFAULT_WORKERS, handle=None):
        positive_workers(workers)
        try:
            if handle:
                return self._put_once(local_path, remote_path, workers, handle)
            return self._put_once(local_path, remote_path, workers)
        except DfshaHttpError as error:
            if error.status != 410 or handle:
                raise
            self._progress("La reserva venció; se pide un plan nuevo una vez")
            return self._put_once(local_path, remote_path, workers)

    def _put_once(self, local_path, remote_path, workers, handle=None):
        before = os.stat(local_path)
        digest = file_sha256(local_path)
        body = {"path": self.path(remote_path), "size": before.st_size, "sha256": digest}
        if handle:
            body["handle"] = handle
        plan = self._control("POST", "/files/upload/plan", json_body=body)
        block_size = plan["block_size"]
        if not isinstance(block_size, int) or not 0 < block_size <= MAX_BLOCK_SIZE:
            raise RuntimeError("tamaño de bloque inválido en el plan")
        self._progress(f"Plan: {plan['n_blocks']} bloques de hasta {block_size} bytes")
        stored_blocks = []
        stop_renewal = threading.Event()
        reservation_lock = threading.Lock()
        current_blocks = {block["index"]: block for block in plan["blocks"]}
        renewal_errors = []

        def renew():
            while not stop_renewal.wait(UPLOAD_RENEW_SECONDS):
                try:
                    fresh = self._control("POST", "/files/upload/renew", json_body={"upload_id": plan["upload_id"]}, timeout=10)
                    if fresh["upload_id"] != plan["upload_id"] or fresh["block_size"] != block_size:
                        raise RuntimeError("la renovación cambió la identidad de la reserva")
                    with reservation_lock:
                        current_blocks.update((block["index"], block) for block in fresh["blocks"])
                except Exception as error:
                    with reservation_lock:
                        renewal_errors.append(error)
                    return

        def upload(block):
            with reservation_lock:
                if renewal_errors:
                    raise renewal_errors[0]
                fresh = current_blocks[block["index"]]
            return self._upload_block(local_path, block_size, fresh)

        renewal_thread = threading.Thread(target=renew, name="dfsha-upload-lease", daemon=True)
        renewal_thread.start()
        try:
            with ThreadPoolExecutor(max_workers=workers) as pool:
                futures = [pool.submit(upload, block) for block in plan["blocks"]]
                for future in as_completed(futures):
                    block = future.result()
                    stored_blocks.append(block)
                    self._progress(f"  bloque {block['index']} -> {', '.join(block['stored_on'])}")
            after = os.stat(local_path)
            if (before.st_size, before.st_mtime_ns) != (after.st_size, after.st_mtime_ns):
                raise RuntimeError("el archivo local cambió durante la subida; no se publica")
            with reservation_lock:
                if renewal_errors:
                    raise renewal_errors[0]
            stored_blocks.sort(key=lambda block: block["index"])
            commit = self._control("POST", "/files/upload/commit", json_body={
                "upload_id": plan["upload_id"], "blocks": stored_blocks, "sha256": digest,
            })
        except Exception:
            try:
                self._control("POST", "/files/upload/abort", json_body={"upload_id": plan["upload_id"]})
            except Exception:
                pass  # Conservar el error original; la reserva tiene vencimiento.
            raise
        finally:
            stop_renewal.set()
            renewal_thread.join()
        self._print(commit)
        return commit

    def _upload_block(self, local_path, block_size, block_plan):
        """Envía el bloque al primer destino, que lo propaga a los demás.

        Si un destino no responde después de los reintentos, el siguiente del
        plan pasa a ser el primero. El ControlNode publica las copias que sí
        quedaron y la reparación repone las demás.
        """
        data = read_block(local_path, block_plan["index"], block_size)
        block_id = hashlib.sha256(data).hexdigest()
        targets = block_plan["targets"]
        if not targets:
            raise RuntimeError("bloque sin destinos autorizados")
        last_error = None
        for position, first in enumerate(targets):
            others = targets[position + 1:]
            headers = self._node_headers(first)
            headers["Content-Type"] = "application/octet-stream"
            if others:
                headers["X-Forward-To"] = json.dumps(others, separators=(",", ":"))
            try:
                response = with_network_retries(
                    lambda: self._request("PUT", self._block_url(first, block_id), data=data, headers=headers, timeout=BLOCK_TIMEOUT_SECONDS),
                    f"bloque {block_plan['index']} -> {first['node']}",
                )
            except NETWORK_ERRORS as error:
                last_error = error
                self._progress(f"  {first['node']} no responde; se usa el siguiente destino del plan")
                continue
            stored_on = response.get("stored_on")
            if not isinstance(stored_on, list) or not stored_on:
                raise RuntimeError("el DataNode no confirmó réplicas almacenadas")
            return {"index": block_plan["index"], "block_id": block_id, "size": len(data), "stored_on": stored_on}
        raise last_error

    def _download_plan(self, remote_path, handle=None):
        query = {"path": self.path(remote_path)}
        if handle:
            query["handle"] = handle
        return self._control("GET", "/files/download/plan", query=query)

    @staticmethod
    def _validate_plan(plan):
        block_size = plan["block_size"]
        if not isinstance(block_size, int) or not 0 < block_size <= MAX_BLOCK_SIZE:
            raise RuntimeError("tamaño de bloque inválido en el plan")
        blocks = sorted(plan["blocks"], key=lambda block: block["index"])
        total = 0
        for index, block in enumerate(blocks):
            empty_file_block = (plan["size"] == 0 and len(blocks) == 1 and block["size"] == 0
                                and block["block_id"] == hashlib.sha256(b"").hexdigest()
                                and bool(block["targets"]))
            if block["index"] != index or (block["size"] <= 0 and not empty_file_block) or block["size"] > block_size:
                raise RuntimeError("plan de bloques incompleto o inválido")
            if index < len(blocks) - 1 and block["size"] != block_size:
                raise RuntimeError("bloque intermedio incompleto en el plan")
            total += block["size"]
        if total != plan["size"]:
            raise RuntimeError("el tamaño del plan no coincide con sus bloques")
        return blocks

    def _read_block_resolver(self, remote_path, handle, plan):
        """Renueva capacidades de lectura conservando la versión inicial."""
        lock = threading.Lock()
        current = {"time": time.monotonic(), "blocks": {block["index"]: block for block in plan["blocks"]}}

        def resolve(block):
            with lock:
                if time.monotonic() - current["time"] >= READ_REFRESH_SECONDS:
                    fresh = self._download_plan(remote_path, handle)
                    self._validate_plan(fresh)
                    identity = lambda item: (item["index"], item["block_id"], item["size"])
                    old_blocks = sorted(identity(item) for item in plan["blocks"])
                    new_blocks = sorted(identity(item) for item in fresh["blocks"])
                    if (fresh["sha256"], fresh["size"], fresh["block_size"], new_blocks) != (plan["sha256"], plan["size"], plan["block_size"], old_blocks):
                        raise RuntimeError("el archivo remoto cambió durante la lectura; vuelve a descargarlo")
                    current.update(time=time.monotonic(), blocks={item["index"]: item for item in fresh["blocks"]})
                return current["blocks"][block["index"]]
        return resolve

    def get(self, remote_path, local_path, workers=DEFAULT_WORKERS, handle=None):
        positive_workers(workers)
        plan = self._download_plan(remote_path, handle)
        blocks = self._validate_plan(plan)
        resolve = self._read_block_resolver(remote_path, handle, plan)
        destination = os.path.abspath(os.fspath(local_path))
        descriptor, partial = tempfile.mkstemp(prefix=".dfsha-", suffix=".part", dir=os.path.dirname(destination))
        try:
            with os.fdopen(descriptor, "wb") as target:
                target.truncate(plan["size"])
            with ThreadPoolExecutor(max_workers=workers) as pool:
                def download(block):
                    return self._download_block_into(partial, plan["block_size"], resolve(block))
                futures = [pool.submit(download, block) for block in blocks]
                for future in as_completed(futures):
                    index, node = future.result()
                    self._progress(f"  bloque {index} <- {node}")
            if file_sha256(partial) != plan["sha256"]:
                raise RuntimeError("SHA-256 del archivo completo inválido; se conserva el destino anterior")
            os.replace(partial, destination)
        finally:
            if os.path.exists(partial):
                os.unlink(partial)
        self._progress(f"Archivo reconstruido en {destination} ({plan['size']} bytes)")
        return {"path": destination, "size": plan["size"], "sha256": plan["sha256"]}

    def _download_block_into(self, partial_path, block_size, block):
        index, data, node = self._download_block(block)
        write_block(partial_path, index, block_size, data)
        return index, node

    def _download_block(self, block, byte_range=None):
        last_error = None
        for target in block["targets"]:
            headers = self._node_headers(target)
            if byte_range is not None:
                headers["Range"] = f"bytes={byte_range[0]}-{byte_range[1]}"
            try:
                data = self._request("GET", self._block_url(target, block["block_id"]), headers=headers, timeout=BLOCK_TIMEOUT_SECONDS)
                if not isinstance(data, bytes):
                    raise RuntimeError("respuesta binaria inválida")
                if byte_range is None:
                    if len(data) != block["size"] or hashlib.sha256(data).hexdigest() != block["block_id"]:
                        raise RuntimeError("integridad de bloque inválida")
                elif len(data) == block["size"]:
                    # Servidores que ignoran Range: verificar antes de recortar.
                    if hashlib.sha256(data).hexdigest() != block["block_id"]:
                        raise RuntimeError("integridad de bloque inválida")
                    data = data[byte_range[0]:byte_range[1] + 1]
                elif len(data) != byte_range[1] - byte_range[0] + 1:
                    raise RuntimeError("longitud de rango inválida")
                return block["index"], data, target["node"]
            except DfshaHttpError as error:
                if error.status == 401:
                    raise
                last_error = error
            except (RuntimeError,) + NETWORK_ERRORS as error:
                last_error = error
        raise last_error or RuntimeError("sin réplicas disponibles")

    # RF3: leases de escritura y acceso a rangos.
    def open(self, path, mode="r"):
        if mode not in ("r", "w"):
            raise ValueError("modo debe ser r o w")
        absolute = self.path(path)
        result = self._control("POST", "/files/open", json_body={"path": absolute, "mode": mode})
        self.handles[result["handle"]] = dict(path=absolute, mode=mode, expires_at=result.get("expires_at"))
        self._save_session()
        self._print(result)
        return result

    def lock(self, path):
        absolute = self.path(path)
        result = self._control("POST", "/files/lock", json_body={"path": absolute})
        self.handles[result["handle"]] = dict(path=absolute, mode="w", expires_at=result.get("expires_at"))
        self._save_session()
        self._print(result)
        return result

    def close(self, handle):
        result = self._control("POST", "/files/close", json_body={"handle": handle})
        self.handles.pop(handle, None)
        self._save_session()
        self._print(result)
        return result

    def read(self, path, offset, length, output=None, handle=None):
        if offset < 0 or length < 0:
            raise ValueError("offset y length deben ser no negativos")
        plan = self._download_plan(path, handle)
        blocks = self._validate_plan(plan)
        resolve = self._read_block_resolver(path, handle, plan)
        end = min(offset + length, plan["size"])
        partial, target = None, None
        try:
            if output is not None:
                destination = os.path.abspath(os.fspath(output))
                descriptor, partial = tempfile.mkstemp(prefix=".dfsha-range-", dir=os.path.dirname(destination))
                target = os.fdopen(descriptor, "wb")
            else:
                target = sys.stdout.buffer
            written = 0
            for block in blocks:
                if end <= offset:
                    break
                start = block["index"] * plan["block_size"]
                block_end = start + block["size"]
                if start >= end or block_end <= offset:
                    continue
                first, last = max(offset, start) - start, min(end, block_end) - start - 1
                _, data, _ = self._download_block(resolve(block), (first, last))
                target.write(data)
                written += len(data)
            target.flush()
            if partial:
                target.close()
                os.replace(partial, destination)
            return {"offset": offset, "size": written, "path": plan["path"]}
        finally:
            if partial:
                if target is not None and not target.closed:
                    target.close()
                if os.path.exists(partial):
                    os.unlink(partial)

    def write(self, handle, local_path, offset=0, workers=DEFAULT_WORKERS):
        if offset < 0:
            raise ValueError("offset debe ser no negativo")
        record = self.handles.get(handle)
        if not record or record.get("mode") != "w":
            raise ValueError("handle escritor desconocido; utiliza open PATH --mode w o lock PATH")
        descriptor, temporary = tempfile.mkstemp(prefix="dfsha-write-")
        os.close(descriptor)
        try:
            try:
                self.get(record["path"], temporary, workers, handle=handle)
            except DfshaHttpError as error:
                if error.status != 404:
                    raise
            with open(temporary, "r+b") as target, open(local_path, "rb") as source:
                target.seek(offset)
                for chunk in iter(lambda: source.read(1024 * 1024), b""):
                    target.write(chunk)
            return self.put(temporary, record["path"], workers, handle=handle)
        finally:
            os.unlink(temporary)


def build_parser():
    parser = argparse.ArgumentParser(description="Cliente CLI de DFSha (TLS, bloques y failover)")
    parser.add_argument("--control", help="URLs de ControlNodes separadas por comas; también DFSHA_CONTROLS")
    commands = parser.add_subparsers(dest="cmd", required=True)
    login = commands.add_parser("login")
    login.add_argument("username")
    login.add_argument("password", nargs="?", help="se solicita de forma oculta si se omite")
    commands.add_parser("pwd")
    commands.add_parser("status")
    commands.add_parser("cd").add_argument("path", nargs="?", default="/")
    commands.add_parser("ls").add_argument("path", nargs="?", default=".")
    for command in ("mkdir", "rmdir", "rm", "stat", "lock"):
        commands.add_parser(command).add_argument("path")
    mv = commands.add_parser("mv")
    mv.add_argument("src")
    mv.add_argument("dst")
    put = commands.add_parser("put", aliases=["send"])
    put.add_argument("local")
    put.add_argument("remote")
    put.add_argument("--workers", type=positive_workers, default=DEFAULT_WORKERS)
    put.add_argument("--handle")
    get = commands.add_parser("get", aliases=["receive", "recv"])
    get.add_argument("remote")
    get.add_argument("local")
    get.add_argument("--workers", type=positive_workers, default=DEFAULT_WORKERS)
    get.add_argument("--handle")
    opened = commands.add_parser("open")
    opened.add_argument("path")
    opened.add_argument("--mode", choices=("r", "w"), default="r")
    commands.add_parser("close").add_argument("handle")
    read = commands.add_parser("read")
    read.add_argument("path")
    read.add_argument("offset", type=int)
    read.add_argument("length", type=int)
    read.add_argument("--output")
    read.add_argument("--handle")
    write = commands.add_parser("write")
    write.add_argument("handle")
    write.add_argument("local")
    write.add_argument("--offset", type=int, default=0)
    write.add_argument("--workers", type=positive_workers, default=DEFAULT_WORKERS)
    return parser


def main():
    args = build_parser().parse_args()
    try:
        client = DfshaClient(args.control)
        if args.cmd == "login":
            client.login(args.username, args.password)
        elif not client.token:
            raise ValueError("Primero ejecuta: python client/dfsha.py login USUARIO")
        elif args.cmd == "pwd":
            print(client.cwd)
        elif args.cmd == "status":
            client.status()
        elif args.cmd in ("ls", "cd", "mkdir", "rmdir", "rm", "stat", "lock"):
            getattr(client, args.cmd)(args.path)
        elif args.cmd == "mv":
            client.mv(args.src, args.dst)
        elif args.cmd in ("put", "send"):
            client.put(args.local, args.remote, args.workers, args.handle)
        elif args.cmd in ("get", "receive", "recv"):
            client.get(args.remote, args.local, args.workers, args.handle)
        elif args.cmd == "open":
            client.open(args.path, args.mode)
        elif args.cmd == "close":
            client.close(args.handle)
        elif args.cmd == "read":
            client.read(args.path, args.offset, args.length, args.output, args.handle)
        elif args.cmd == "write":
            client.write(args.handle, args.local, args.offset, args.workers)
    except (OSError, RuntimeError, ValueError) as error:
        print(f"DFSha: {error}", file=sys.stderr)
        return 1
    except KeyboardInterrupt:
        print("DFSha: operación interrumpida", file=sys.stderr)
        return 130
    return 0


if __name__ == "__main__":
    sys.exit(main())
