#!/usr/bin/env sh
set -eu
# Requiere Docker Compose. Ejecutar desde la raíz del proyecto.
docker compose up -d --build controlnode dn1 dn2 dn3
sleep 5
printf 'DFSha Hito 2\n%.0s' 1 2 3 4 5 > demo.txt

docker compose --profile tools run --rm client login demo demo
# El contenedor es efímero, por eso para el demo usamos el token por variable.
export DFSHA_TOKEN=dfsha-dev-token

docker compose --profile tools run --rm -e DFSHA_TOKEN=$DFSHA_TOKEN client mkdir /demo || true
docker compose --profile tools run --rm -e DFSHA_TOKEN=$DFSHA_TOKEN -v "$PWD:/host" client put /host/demo.txt /demo/demo.txt
docker compose --profile tools run --rm -e DFSHA_TOKEN=$DFSHA_TOKEN client ls /demo
docker compose --profile tools run --rm -e DFSHA_TOKEN=$DFSHA_TOKEN -v "$PWD:/host" client get /demo/demo.txt /host/demo-descargado.txt
sha256sum demo.txt demo-descargado.txt
