#!/usr/bin/env python3
"""Demostración portable contra el Compose ya iniciado; no imprime credenciales."""
import argparse
from pathlib import Path
import subprocess
import sys


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--size-mib", type=int, default=24, help="tamaño binario de prueba (1 a 512 MiB)")
    args = parser.parse_args()
    if not 1 <= args.size_mib <= 512:
        parser.error("--size-mib debe estar entre 1 y 512")
    root = Path(__file__).resolve().parent.parent
    command = ["docker", "compose", "--profile", "tools", "run", "--rm", "-T",
               "-e", "DFSHA_SESSION=/tmp/dfsha-demo-session.json", "--entrypoint", "python", "client",
               "/work/scripts/docker_demo.py", "--size-mib", str(args.size_mib)]
    try:
        return subprocess.run(command, cwd=root).returncode
    except OSError as error:
        print(f"No se pudo ejecutar Docker: {error}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    sys.exit(main())
