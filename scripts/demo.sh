#!/usr/bin/env sh
# Demostración de punta a punta del Hito 2. Ejecutar desde la raíz del repositorio.
#   1. Levanta el clúster (1 ControlNode + 3 DataNodes).
#   2. Sube un archivo de 40 MB, que se parte en varios bloques con 2 copias cada uno.
#   3. Muestra que una segunda reserva sobre la misma ruta se rechaza con 409.
#   4. Apaga un DataNode y descarga el archivo usando las réplicas restantes.
#   5. Agrega un cuarto DataNode en caliente y muestra que recibe bloques nuevos.
#   6. Compara la firma SHA-256 del original con las de las descargas.
set -eu

WORKDIR=demo-files
TOKEN=dfsha-dev-token
CONTROL=http://localhost:8000

dfsha() {
  docker compose --profile tools run --rm -e DFSHA_TOKEN="$TOKEN" -v "$PWD/$WORKDIR:/host" client "$@"
}

upload_plan_status() {
  curl -s -o /dev/null -w "%{http_code}\n" -X POST \
    -H "Authorization: Bearer $TOKEN" \
    -d '{"path":"/demo/reservado.bin","size":10}' \
    "$CONTROL/files/upload/plan"
}

echo "== 1. Levantar el clúster"
docker compose up -d --build controlnode dn1 dn2 dn3
sleep 5
curl -s "$CONTROL/health"; echo

echo "== 2. Subir un archivo de 40 MB"
mkdir -p "$WORKDIR"
head -c 40000000 /dev/urandom > "$WORKDIR/original.bin"
dfsha login demo demo
dfsha mkdir /demo || true
dfsha put /host/original.bin /demo/original.bin
dfsha ls /demo
dfsha stat /demo/original.bin
dfsha get /demo/original.bin /host/descargado.bin

echo "== 3. Un archivo lo escribe un solo cliente a la vez"
echo "primer plan:  $(upload_plan_status)   (esperado 200)"
echo "segundo plan: $(upload_plan_status)   (esperado 409)"

echo "== 4. Tolerancia a fallos: se apaga dn1"
docker compose stop dn1
sleep 10 # heartbeat_interval x heartbeat_misses = 9 s
curl -s "$CONTROL/health"; echo
dfsha get /demo/original.bin /host/descargado-sin-dn1.bin
docker compose start dn1

echo "== 5. Escalado: se agrega dn4 con el clúster en marcha"
docker compose --profile scale up -d dn4
sleep 5
curl -s "$CONTROL/health"; echo
head -c 40000000 /dev/urandom > "$WORKDIR/despues-de-dn4.bin"
dfsha put /host/despues-de-dn4.bin /demo/despues-de-dn4.bin

echo "== 6. Las tres firmas deben ser iguales"
sha256sum "$WORKDIR/original.bin" "$WORKDIR/descargado.bin" "$WORKDIR/descargado-sin-dn1.bin"
