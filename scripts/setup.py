#!/usr/bin/env python3
"""Genera los secretos del clúster: CA, certificado, claves y el usuario inicial.

Por defecto compila y ejecuta cmd/admin dentro de un contenedor de Go, así que
solo necesita Docker; con --native usa el Go instalado. La contraseña no se
imprime: queda en <carpeta>/credentials.txt, legible solo por el usuario.
"""
import argparse
import os
from pathlib import Path
import shutil
import subprocess
import sys

DEFAULT_HOSTS = "localhost,127.0.0.1,cn1,cn2,cn3,dn1,dn2,dn3,dn4"


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--native", action="store_true", help="ejecutar go run localmente")
    parser.add_argument("--go", default=os.environ.get("GO_BINARY", "go"), help="ejecutable Go para --native")
    parser.add_argument("--out", default=".secrets", help="carpeta de salida, relativa al repositorio (por defecto .secrets)")
    parser.add_argument("--hosts", default="", help="IP o nombres adicionales para el certificado, separados por comas (AWS)")
    parser.add_argument("--user", default="demo", help="usuario inicial o usuario a agregar")
    parser.add_argument("--add-user", action="store_true", help="agregar usuario sin regenerar claves")
    parser.add_argument("--password-file", help="archivo privado con contraseña de 12 a 72 bytes; aleatoria si se omite")
    args = parser.parse_args()
    root = Path(__file__).resolve().parent.parent
    administrator = ["go", "run", "./cmd/admin", "--out", args.out, "--hosts", DEFAULT_HOSTS + ("," + args.hosts if args.hosts else ""), "--user", args.user]
    if args.add_user:
        administrator.append("--add-user")
    password_path = None
    if args.password_file:
        password_path = Path(args.password_file).expanduser().resolve()
        if not password_path.is_file():
            parser.error("--password-file debe apuntar a un archivo existente")
    if args.native:
        if not shutil.which(args.go):
            parser.error("Go no está disponible; instala Go >=1.25 o utiliza el modo Docker")
        administrator[0] = args.go
        if password_path:
            administrator.extend(["--password-file", str(password_path)])
        command = administrator
    else:
        if not shutil.which("docker"):
            parser.error("Docker no está disponible; instala Docker Desktop/Engine o utiliza --native")
        command = ["docker", "run", "--rm", "-v", str(root) + ":/src", "-w", "/src"]
        # Evita crear ficheros propiedad de root en una estación Linux.
        if hasattr(os, "getuid"):
            command.extend(["--user", f"{os.getuid()}:{os.getgid()}", "-e", "GOCACHE=/tmp/go-build", "-e", "GOPATH=/tmp/go"])
        if password_path:
            command.extend(["-v", str(password_path) + ":/run/password:ro"])
            administrator.extend(["--password-file", "/run/password"])
        command.extend(["golang:1.26-alpine"] + administrator)
    try:
        subprocess.run(command, cwd=root, check=True)
    except (OSError, subprocess.CalledProcessError) as error:
        print(f"Inicialización fallida: {error}", file=sys.stderr)
        return 1
    print(f"Listo. La contraseña inicial está en {args.out}/credentials.txt.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
