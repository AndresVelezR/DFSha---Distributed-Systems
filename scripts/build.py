#!/usr/bin/env python3
"""Compila admin, controlnode y datanode en bin/. Requiere Go 1.25 o superior."""
import argparse
import os
from pathlib import Path
import shutil
import subprocess
import sys

ROOT = Path(__file__).resolve().parents[1]


def find_go(explicit=None):
    candidate = explicit or os.environ.get("GO_BINARY") or shutil.which("go")
    if not candidate:
        raise RuntimeError("Go no está en el PATH; instálalo o indica --go RUTA")
    return candidate


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--go", help="ruta del ejecutable de Go")
    args = parser.parse_args()
    try:
        go = find_go(args.go)
        (ROOT / "bin").mkdir(exist_ok=True)
        suffix = ".exe" if os.name == "nt" else ""
        for component in ("admin", "controlnode", "datanode"):
            subprocess.run([go, "build", "-trimpath", "-o", str(ROOT / "bin" / (component + suffix)), "./cmd/" + component],
                           cwd=ROOT, check=True)
            print("Compilado:", component)
    except (OSError, RuntimeError, subprocess.CalledProcessError) as error:
        print(error, file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
