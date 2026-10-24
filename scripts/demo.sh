#!/usr/bin/env sh
# Compatibilidad con la demostración original; requiere el Compose iniciado.
set -eu
exec python3 scripts/demo.py "$@"
