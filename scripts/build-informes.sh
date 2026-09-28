#!/usr/bin/env sh
# Compila a PDF el informe de cada hito que tenga fuente LaTeX:
#   docs/hito-N/hito-N-*.tex  ->  docs/hito-N/hito-N-*.pdf
# Requiere TeX Live con LuaLaTeX. Ejecutar desde la raíz del repositorio.
set -eu

ROOT=$(pwd)
BUILD=$(mktemp -d)
trap 'rm -rf "$BUILD"' EXIT

for tex in docs/hito-*/hito-*.tex; do
  [ -e "$tex" ] || continue
  name=$(basename "$tex" .tex)
  echo "Compilando $tex"
  # Dos pasadas: la primera calcula el índice y la segunda lo imprime.
  for pass in 1 2; do
    TEXINPUTS="$ROOT/docs/plantilla//:" lualatex -interaction=nonstopmode -halt-on-error \
      -output-directory="$BUILD" "$tex" >/dev/null || {
        echo "Error compilando $tex; revisa $BUILD/$name.log" >&2
        trap - EXIT
        exit 1
      }
  done
  cp "$BUILD/$name.pdf" "$(dirname "$tex")/$name.pdf"
  echo "  -> $(dirname "$tex")/$name.pdf"
done
