# Cómo ejecutar DFSha y comprobar que funciona

## 1. Requisitos

Instala Git, Python 3.10 o superior y Docker Desktop/Engine con Compose. En Windows abre Docker Desktop y usa contenedores Linux. En Linux puedes sustituir `python` por `python3`. Ejecuta todo desde la carpeta del proyecto.

```sh
docker version
docker compose version
python --version
```

Si Docker muestra error de conexión al motor, inicia Docker Desktop antes de continuar.

## 2. Preparar y arrancar

En una copia nueva:

```sh
python scripts/setup.py
docker compose --profile tools build
docker compose up -d cn1 cn2 cn3 dn1 dn2 dn3
docker compose ps
```

Setup genera claves y certificados en `.secrets`. **Si ya existen secretos y datos, omite setup.** Espera seis servicios `healthy`. La primera construcción tarda mientras descarga dependencias. No necesitas Go instalado para este arranque.

## 3. Comprobar automáticamente

```sh
python scripts/demo.py
```

Debe mostrar `PASS` para autenticación, archivo de 24 MiB en varios bloques, réplicas, descarga y SHA-256, rangos, bloqueo/escritura, archivo vacío y gestión de archivos. Guarda evidencia en `demo-files/` sin imprimir secretos.

## 4. Uso manual

Abre localmente `.secrets/credentials.txt`: cada línea contiene `usuario contraseña`. El usuario inicial es `demo`. No publiques ese archivo ni lo muestres en el video. Cuando el cliente solicite contraseña, escribe solo la contraseña.

```sh
docker compose --profile tools run --rm client login demo
docker compose --profile tools run --rm client mkdir /mis-archivos
docker compose --profile tools run --rm client put /work/README.md /mis-archivos/README.md
docker compose --profile tools run --rm client ls /mis-archivos
docker compose --profile tools run --rm client stat /mis-archivos/README.md
docker compose --profile tools run --rm client get /mis-archivos/README.md /work/demo-files/README-descargado.md
docker compose --profile tools run --rm client status
```

El proyecto se monta como `/work`; coloca allí tus archivos. La demo crea `demo-files` para las descargas. La sesión persiste en un volumen. Consulta [README.md](README.md) para `cd`, `mv`, `rm`, `rmdir`, `open`, `close`, `read`, `write` y `lock`.

## 5. Fallos y crecimiento

```sh
docker compose stop dn1
# Repite get: debe usar otra copia.
docker compose start dn1
docker compose stop cn1
# Repite stat/get; espera elección si cn1 era líder.
docker compose start cn1
docker compose --profile scale up -d dn4
docker compose --profile tools run --rm client status
```

La detección normal de DN caído tarda alrededor de nueve segundos y la reparación es gradual. Se necesitan dos de los tres controles. El [guion](docs/VIDEO.md) explica la sustentación.

## 6. Apagar y reanudar

```sh
docker compose stop
docker compose start
```

Esto conserva archivos. `docker compose down` también conserva volúmenes. **No uses `down -v` ni borres `.secrets` si necesitas los datos.**

## 7. Pruebas completas y AWS

Con Go >=1.25 instalado:

```sh
python scripts/test.py
python scripts/integration_test.py --extended
```

Las pruebas usan procesos/datos temporales y dejan `docs/resultados/integration.json`. La extendida tarda varios minutos por vencimientos y GC reales. Para tres EC2 sigue [deploy/aws/README.md](deploy/aws/README.md).

## Problemas frecuentes

- Motor Docker inaccesible: abre Docker Desktop y espera que arranque.
- Certificado inválido: usa los generados por esta versión. Con Go, `go run ./cmd/admin --renew-cert` reemite el certificado conservando datos; incluye SAN personalizados en `--hosts` y reinicia los nodos.
- HTTP 503: espera elección y comprueba dos controles activos. Revisa `docker compose logs --tail 50 cn1 cn2 cn3`.
- HTTP 401: vuelve a iniciar sesión; expira después de una hora.
- HTTP 409: la ruta existe o hay escritor activo; cierra su handle o espera vencimiento.
- HTTP 507: falta capacidad bajo el umbral; agrega DN o libera archivos y espera GC.
- La CLI nativa no resuelve dn1: usa el cliente de Compose que comparte su red.
