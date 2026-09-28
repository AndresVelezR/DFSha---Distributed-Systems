# DFSha — Sistema de archivos distribuido

Sistema de archivos distribuido **por bloques**, con alta disponibilidad, rendimiento y seguridad. Proyecto 1 de ST0263 Tópicos Especiales en Telemática / SI3007 Sistemas Distribuidos, Universidad EAFIT, 2026-2.

Un archivo nunca se guarda completo en una sola máquina ni se carga completo en memoria: el cliente lo parte en bloques, cada bloque se guarda en varios DataNodes, y al descargarlo se reconstruye verificando la firma SHA-256 de cada bloque.

**Autores:** Andrés Vélez Rendón, Jose Luis Restrepo.

## Entregas

Cada entrega tiene su carpeta en `docs/` y su informe en PDF, todos con el mismo formato.

| Hito | Contenido | Informe | Estado |
|---|---|---|---|
| 1 | Especificación del proyecto | [`docs/hito-1/hito-1-especificacion.pdf`](docs/hito-1/hito-1-especificacion.pdf) | Entregado |
| 2 | Arquitectura distribuida y comunicaciones | [`docs/hito-2/hito-2-arquitectura-y-comunicaciones.pdf`](docs/hito-2/hito-2-arquitectura-y-comunicaciones.pdf) | Entregado, etiqueta `hito-2` |
| 3 | Alta disponibilidad, replicación, consistencia y seguridad | — | Pendiente |
| 4 | Entrega final | — | Pendiente |

Los informes desde el Hito 2 se escriben en LaTeX con la plantilla común [`docs/plantilla/dfsha-informe.sty`](docs/plantilla/dfsha-informe.sty) y se compilan con `./scripts/build-informes.sh` (requiere TeX Live con LuaLaTeX).

## Arquitectura

Cliente/Servidor con el servicio distribuido (Opción 1), patrón Maestro–Trabajador:

```text
                        control / metadatos
+----------------+   REST/JSON   +------------------+
| Cliente CLI    | ------------> | ControlNode      |
| (Python)       | <------------ | :8000            |
+-------+--------+               +---------+--------+
        |                                   ^
        | bloques binarios                  | register / heartbeat
        |                                   |
        +----------+-------------+----------+
                   |             |
             +-----v----+  +-----v----+  +-----v----+
             | dn-01    |  | dn-02    |  | dn-03    |
             | :8001    |  | :8001    |  | :8001    |
             +-----+----+  +-----+----+  +-----+----+
                   \___________|____________/
                     réplica entre DataNodes
```

- **ControlNode** (Go): metadatos, DataNodes vivos, planes de lectura y escritura. Nunca recibe datos de archivos.
- **DataNode** (Go): guarda bloques en disco y propaga copias a otros DataNodes.
- **Cliente** (Python, sin dependencias): parte, sube, baja y reconstruye archivos. No tiene direcciones de DataNodes configuradas: las recibe en cada plan.

El detalle de la arquitectura, los algoritmos, la API y las pruebas está en el [informe del Hito 2](docs/hito-2/hito-2-arquitectura-y-comunicaciones.pdf).

## Estructura del repositorio

```text
.
├── cmd/
│   ├── controlnode/     ControlNode: configuración, planificador y endpoints HTTP
│   └── datanode/        DataNode: almacenamiento de bloques y comunicación con otros nodos
├── client/              Cliente CLI y sus pruebas
├── docker/              Un Dockerfile por componente
├── deploy/aws/          Despliegue en varias VMs (AWS Academy)
├── docs/
│   ├── hito-1/          Informe del Hito 1
│   ├── hito-2/          Informe del Hito 2 (PDF y fuente LaTeX)
│   └── plantilla/       Formato común de los informes
├── scripts/
│   ├── demo.sh          Demostración de punta a punta
│   └── build-informes.sh
├── docker-compose.yml   Clúster local: 1 ControlNode + 3 DataNodes (+1 opcional)
└── go.mod
```

## Inicio rápido

Requiere Docker con Docker Compose. Todo se ejecuta desde la raíz del repositorio.

La forma más rápida de verlo funcionar es la demostración completa:

```bash
./scripts/demo.sh
```

Levanta el clúster y ejecuta, en orden:

1. Sube un archivo de 40 MB y lo descarga.
2. Prueba la reserva de escritura.
3. Apaga un DataNode y vuelve a descargar desde las réplicas.
4. Agrega un cuarto DataNode en caliente.
5. Compara las firmas SHA-256.

### Paso a paso con Docker

```bash
docker compose up -d --build controlnode dn1 dn2 dn3
curl http://localhost:8000/health        # debe reportar alive_datanodes: 3
```

El cliente corre como contenedor dentro de la red del clúster. Para no repetir las opciones, conviene un alias:

```bash
alias dfsha='docker compose --profile tools run --rm -e DFSHA_TOKEN=dfsha-dev-token -v "$PWD:/host" client'

dfsha mkdir /demo
dfsha put /host/archivo.bin /demo/archivo.bin
dfsha ls /demo
dfsha stat /demo/archivo.bin
dfsha get /demo/archivo.bin /host/archivo-descargado.bin
sha256sum archivo.bin archivo-descargado.bin
```

Tolerancia a fallos y escalado:

```bash
docker compose stop dn1
sleep 10                                  # el ControlNode lo da por muerto a los 9 s
dfsha get /demo/archivo.bin /host/sin-dn1.bin
docker compose start dn1

docker compose --profile scale up -d dn4  # agrega un DataNode sin reiniciar nada
```

Para apagar todo y borrar los datos: `docker compose --profile scale down -v`.

### Sin Docker

Requiere Go 1.23 o superior y Python 3.

```bash
go build -o controlnode ./cmd/controlnode && ./controlnode &
go build -o datanode ./cmd/datanode
NODE_ID=dn-01 PORT=8001 STORAGE_DIR=./dn1 ./datanode &
NODE_ID=dn-02 PORT=8002 STORAGE_DIR=./dn2 ./datanode &
NODE_ID=dn-03 PORT=8003 STORAGE_DIR=./dn3 ./datanode &

python3 client/dfsha.py login demo demo
python3 client/dfsha.py put archivo.bin /archivo.bin
```

### En varias máquinas (AWS Academy)

Ver [`deploy/aws/README.md`](deploy/aws/README.md): un mismo `docker-compose.yml` para todas las VMs y un `.env` por VM.

## Cliente

| Comando | Qué hace |
|---|---|
| `login <usuario> <contraseña>` | Autentica y guarda el token |
| `ls [ruta]` | Lista un directorio |
| `mkdir <ruta>` / `rmdir <ruta>` | Crea un directorio / borra uno vacío |
| `rm <ruta>` | Borra un archivo |
| `mv <origen> <destino>` | Renombra o mueve un archivo |
| `stat <ruta>` | Tamaño, bloques, copias vivas y dueño |
| `put <local> <remoto> [--workers N]` | Sube un archivo partido en bloques |
| `get <remoto> <local> [--workers N]` | Descarga y reconstruye un archivo |

La dirección del ControlNode se indica con `--control` o con la variable `DFSHA_CONTROL` (por defecto `http://localhost:8000`).

### Credenciales de demostración

| Valor | |
|---|---|
| Usuario / contraseña | `demo` / `demo` |
| Token | `dfsha-dev-token` |
| Clave interna del clúster | `dev-cluster-key` |

Son valores **solo de desarrollo**. La seguridad real (HTTPS, cifrado, usuarios) es del Hito 3.

## Configuración

Los parámetros operativos se leen de variables de entorno, así que se cambian sin recompilar:

```bash
REPLICATION_FACTOR=1 BLOCK_SIZE_MIN_MB=4 docker compose up -d
```

| Variable | Por defecto | Qué controla |
|---|---|---|
| `REPLICATION_FACTOR` | 2 | Copias de cada bloque (nunca más de N − 1 DataNodes) |
| `BLOCK_SIZE_MIN_MB` | 8 | Piso del tamaño de bloque |
| `BLOCK_SIZE_MAX_MB` | 64 | Techo del tamaño de bloque |
| `BLOCKS_PER_NODE` | 4 | Bloques mínimos por nodo que debe producir un archivo |
| `HEARTBEAT_INTERVAL_SECONDS` | 3 | Frecuencia del heartbeat de los DataNodes |
| `HEARTBEAT_MISSES` | 3 | Heartbeats perdidos para dar por muerto un DataNode |
| `WRITE_LEASE_TTL_SECONDS` | 60 | Vigencia de la reserva de escritura de un archivo |

## Pruebas

```bash
go test ./...
python3 -m unittest discover client
```
