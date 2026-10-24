# DFSha — Hito 2 (Semanas 2–3)

Implementación base del **DFS por bloques** definido en el Hito 1. Este entregable se concentra en lo pedido para las semanas 2–3: **arquitectura distribuida de la Opción 1 (Cliente/Servidor con servicio distribuido)** y **especificación/implementación de comunicaciones**.

## Qué está implementado

- `ControlNode` en Go: registro y seguimiento de DataNodes, metadatos en memoria, namespace básico, plan de subida, commit y plan de descarga.
- `DataNode` en Go: almacenamiento de bloques en disco, `PUT/GET/DELETE`, `Range` mediante `http.ServeContent`, heartbeat y propagación DataNode → DataNode.
- Cliente CLI en Python: `login`, `ls`, `mkdir`, `rmdir`, `rm`, `mv`, `stat`, `put`, `get`.
- Particionamiento por bloques de 8–64 MiB y plan calculado por el ControlNode.
- Distribución por menor ocupación con carga virtual durante la planificación.
- Factor de replicación 2 cuando existen al menos dos DataNodes.
- Subidas y descargas concurrentes desde el cliente (`--workers`, 4 por defecto).
- Integridad de bloque con SHA-256, que además funciona como `block_id`.
- Docker Compose con 1 ControlNode activo y 3 DataNodes.

## Qué se deja deliberadamente para Hito 3

El Hito 1 define el diseño final con tres ControlNodes, replicación de metadatos, elección de líder, persistencia fuerte, cifrado en reposo y HTTPS. Esas funciones pertenecen al hito de **Alta Disponibilidad, Replicación, Consistencia y Seguridad**. Por eso en Hito 2 quedan definidos los endpoints `/cluster/replicate` y `/cluster/election`, pero responden `501 Not Implemented` para no presentar como terminado algo que corresponde a semanas 4–5.

## Arquitectura ejecutable de Hito 2

```text
                        control / metadatos
+----------------+   REST/JSON   +------------------+
| Cliente Python | ------------> | ControlNode cn-01|
| CLI            | <------------ | :8000            |
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

El cliente nunca necesita conocer de antemano las direcciones de los DataNodes: pide un plan al ControlNode en cada operación.

## Inicio rápido con Docker

```bash
docker compose up -d --build controlnode dn1 dn2 dn3
```

Comprobar estado:

```bash
curl http://localhost:8000/health
```

Crear un archivo de prueba y subirlo usando el cliente dentro de la red Docker:

```bash
echo "hola DFSha" > demo.txt

docker compose --profile tools run --rm \
  -e DFSHA_TOKEN=dfsha-dev-token \
  client mkdir /demo

docker compose --profile tools run --rm \
  -e DFSHA_TOKEN=dfsha-dev-token \
  -v "$PWD:/host" \
  client put /host/demo.txt /demo/demo.txt
```

Listar y descargar:

```bash
docker compose --profile tools run --rm \
  -e DFSHA_TOKEN=dfsha-dev-token \
  client ls /demo

docker compose --profile tools run --rm \
  -e DFSHA_TOKEN=dfsha-dev-token \
  -v "$PWD:/host" \
  client get /demo/demo.txt /host/demo-descargado.txt
```

Verificación:

```bash
sha256sum demo.txt demo-descargado.txt
```

## Credenciales de demostración

- usuario: `demo`
- contraseña: `demo`
- token emitido: `dfsha-dev-token`
- clave interna de clúster: `dev-cluster-key`

Son valores **solo de desarrollo**. No representan la seguridad final del sistema.

## API implementada

### Cliente → ControlNode

| Método | Ruta | Función |
|---|---|---|
| POST | `/auth/login` | autenticación de demostración |
| GET | `/fs/ls?path=` | listar directorio |
| POST | `/fs/mkdir` | crear directorio |
| DELETE | `/fs/rmdir?path=` | borrar directorio vacío |
| DELETE | `/fs/rm?path=` | borrar metadatos de archivo |
| POST | `/fs/mv` | mover/renombrar archivo |
| GET | `/fs/stat?path=` | metadatos de archivo |
| POST | `/files/upload/plan` | plan de partición y destinos |
| POST | `/files/upload/commit` | publicar archivo tras subir bloques |
| POST | `/files/upload/abort` | cancelar sesión de subida |
| GET | `/files/download/plan?path=` | plan de lectura con réplicas vivas |

### Cliente/DataNode → DataNode

| Método | Ruta | Función |
|---|---|---|
| PUT | `/blocks/{sha256}` | almacenar bloque; opcionalmente propagarlo |
| GET | `/blocks/{sha256}` | recuperar bloque; soporta `Range` |
| DELETE | `/blocks/{sha256}` | borrar bloque, solo tráfico interno |
| POST | `/blocks/{sha256}/replicate` | copiar un bloque a otro DataNode |
| GET | `/health` | salud y uso de disco |

### DataNode → ControlNode

| Método | Ruta | Función |
|---|---|---|
| POST | `/cluster/register` | registro inicial |
| POST | `/cluster/heartbeat` | señal de vida y ocupación |
| POST | `/cluster/blockreport` | contrato para reporte de inventario |

### Reservado para Hito 3

- `POST /cluster/replicate` entre ControlNodes.
- `POST /cluster/election` entre ControlNodes.

## Flujo de subida

1. Cliente solicita `POST /files/upload/plan` con ruta y tamaño.
2. ControlNode valida el namespace y obtiene DataNodes vivos.
3. Calcula `block_size`, número de bloques y destinos por bloque.
4. Cliente corta el archivo y calcula SHA-256 por bloque.
5. Cliente manda cada bloque al primer DataNode; en `X-Forward-To` manda las réplicas adicionales.
6. El primer DataNode persiste el bloque y lo propaga al segundo.
7. Cliente hace `POST /files/upload/commit`.
8. ControlNode publica el archivo en metadatos.

## Flujo de descarga

1. Cliente pide `GET /files/download/plan`.
2. ControlNode devuelve bloques ordenados y réplicas vivas.
3. Cliente descarga varios bloques en paralelo.
4. Si una réplica falla, prueba la siguiente.
5. Verifica SHA-256 y reconstruye por índice.

## Limitaciones conocidas de este hito

- Los metadatos del ControlNode aún están en memoria.
- `rm` borra metadatos pero todavía no programa recolección de bloques huérfanos.
- No hay elección de líder ni réplica de metadatos.
- No hay HTTPS ni cifrado en disco todavía.
- El control de acceso es una autenticación de demostración con un único usuario.

Estas limitaciones están alineadas con el cronograma: el Hito 3 agrega alta disponibilidad, replicación/consistencia y seguridad.
