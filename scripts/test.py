#!/usr/bin/env python3
"""Run unit tests, vet, and the real HTTPS cluster integration suite."""
import argparse
from pathlib import Path
import subprocess
import sys

from build import find_go

ROOT = Path(__file__).resolve().parents[1]


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--go")
    parser.add_argument("--extended", action="store_true")
    args = parser.parse_args()
    go = find_go(args.go)
    commands = [[go, "test", "./..."], [go, "vet", "./..."],
                [sys.executable, "-m", "unittest", "discover", "client", "-v"],
                [sys.executable, "scripts/build.py", "--go", go],
                [sys.executable, "scripts/integration_test.py"]]
    if args.extended:
        commands[-1].append("--extended")
    for command in commands:
        subprocess.run(command, cwd=ROOT, check=True)


if __name__ == "__main__":
    main()
