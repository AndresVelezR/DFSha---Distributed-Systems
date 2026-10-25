#!/usr/bin/env python3
"""Corre todas las pruebas: unitarias de Go y Python, go vet y la integración."""
import argparse
from pathlib import Path
import subprocess
import sys

from build import find_go

ROOT = Path(__file__).resolve().parents[1]


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--go", help="ruta del ejecutable de Go")
    parser.add_argument("--extended", action="store_true", help="incluir los escenarios lentos de la integración")
    args = parser.parse_args()
    go = find_go(args.go)
    commands = [[go, "test", "./..."], [go, "vet", "./..."],
                [sys.executable, "-m", "unittest", "discover", "client", "-v"],
                [sys.executable, "scripts/build.py", "--go", go],
                [sys.executable, "scripts/integration_test.py"] + (["--extended"] if args.extended else [])]
    for command in commands:
        subprocess.run(command, cwd=ROOT, check=True)


if __name__ == "__main__":
    main()
