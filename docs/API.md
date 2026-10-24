# API REST de DFSha

La API conserva las rutas del Hito 1 y añade renovación de leases, handles, estado e inventario. Las rutas REST usan HTTPS. El consenso CN↔CN se ejecuta por transporte Raft con TLS mutuo en el puerto 9000; los endpoints REST históricos de elección y réplica son diagnósticos, no implementaciones paralelas de consenso.

## Convenciones, seguridad y errores comunes

- ControlNode: puerto de referencia `8000`. DataNode: `8001`. Los puertos son configurables.
- Control: JSON UTF-8. Bloques: cuerpo binario `application/octet-stream`, sin base64.
- Rutas remotas: jerarquía absoluta lógica dentro del árbol del usuario; el cliente resuelve rutas relativas contra su `cwd`. El servidor normaliza `.`/`..`, limita la longitud a 4096 y rechaza separadores de Windows, `:`, NUL y saltos de línea.
- Identificadores de bloque: SHA-256 hexadecimal minúscula, exactamente 64 caracteres. Tamaños y offsets son bytes. Fechas de respuesta: RFC 3339; expiración firmada: segundos Unix.
- Sesión de usuario: `Authorization: Bearer <token-de-sesion>`. El token está firmado con HMAC-SHA256 y dura 3600 s; no es un JWT de tres segmentos.
- Operaciones de bloques: `Authorization: Bearer <capability>`, emitida por el ControlNode. **El token de sesión no autoriza directamente un bloque**. Lectura vincula hash y nodo; escritura vincula nodo y tamaño máximo del plan, con hash comodín porque todavía no se conoce el contenido al planificar. El DataNode siempre verifica que el hash de los bytes recibidos coincide con la URL.
- Tráfico interno: `X-Cluster-Key: <secreto>` por HTTPS. Raft exige además certificado de cliente de la CA del clúster.
- JSON de error de aplicación: `{"error":"descripción"}`. El servidor HTTP puede responder un error de método antes de entrar a la aplicación.
- `401`: sesión, firma, expiración o clave interna inválida. `403`: identificador perteneciente a otro usuario. `400`: ruta, ID, modo o JSON inválido. `404`: recurso ausente. `409`: conflicto de reserva, ruta o manifiesto. `410`: lease/handle inválido o vencido. `413`: límites de petición/plan. `503`: sin líder, sin mayoría o bloque sin copias disponibles. `507`: capacidad insuficiente.

Todos los endpoints autenticados del ControlNode requieren liderazgo vigente y mayoría. Un seguidor devuelve `503`, normalmente con `{"error":"líder no disponible en este nodo","leader":"https://..."}`. El cliente puede probar esa dirección y otros controles configurados. `GET /health` solo confirma que el proceso responde; `200` no acredita que pueda confirmar escrituras.

El JSON de entrada al ControlNode se limita a 16 MiB y un plan admite como máximo 100000 bloques. No se debe interpretar la ausencia de un límite de archivo en la arquitectura original como capacidad ilimitada de esta implementación educativa.

## 1. Cliente ↔ ControlNode

El consumidor es la CLI o una aplicación autorizada. Salvo login y health, todas las rutas usan la sesión del propietario. Dos usuarios pueden tener `/demo/a.bin` sin compartir el mismo objeto de metadatos.

### Autenticación y namespace

| Método y ruta | Solicitud | Respuesta correcta | Errores específicos | Reintento e idempotencia |
|---|---|---|---|---|
| `GET /health` | Sin cuerpo ni auth. | `200 {node_id,role,leader_id,leader,applied_index,alive_datanodes}`. | Fallo de conexión si el proceso no responde. | GET repetible. Consultar otro CN si no responde. |
| `POST /auth/login` | `{username,password}`. | `200 {token,expires_in:3600}`. Crea raíz privada si no existía. | `401` credenciales; `503` control no autoritativo. | Puede repetirse; emite otra sesión. No registrar contraseña/token en logs. |
| `GET /fs/ls?path=/demo` | Sin cuerpo. | `200 {path,entries:[{name,type,mode,size?,blocks?,replicas_ok?}]}`. | `404` directorio ausente. | GET repetible; obtener líder vigente ante `503`. |
| `POST /fs/mkdir` | `{path}`. | `201 {path}`. | `404` padre; `409` ruta existente/reservada. | La segunda creación devuelve `409`; consultar antes de tratarlo como fallo. |
| `DELETE /fs/rmdir?path=/demo` | Sin cuerpo. | `204`, sin cuerpo. | `404` ausente; `409` raíz, contenido o acceso/subida activos. | Una segunda eliminación devuelve `404`. No elimina recursivamente. |
| `DELETE /fs/rm?path=/demo/a.bin` | Sin cuerpo. | `204`. Retira publicación; GC posterior conserva referencias activas. | `404`; `409` escritor activo. | Segunda eliminación `404`. Nunca pedir DELETE de bloques desde cliente. |
| `POST /fs/mv` | `{src,dst}`. | `200 {src,dst}`. Mueve archivo o subárbol cambiando metadatos. | `404` origen/padre; `409` destino existente, movimiento cíclico o escritor activo. | No repetir a ciegas si se perdió respuesta; consultar origen y destino. |
| `GET /fs/stat?path=/demo/a.bin` | Sin cuerpo. | Archivo: `200 {path,type:"file",owner,mode:"0600",size,sha256,block_size,n_blocks,replicas_ok,blocks,created_at}`. Directorio: `{path,type:"dir",owner,mode:"0700"}`. | `404`. | GET repetible. `replicas_ok` es el mínimo de copias vivas entre bloques, no un porcentaje. |

No existe endpoint `cd`: la CLI valida directorio con el servicio y cambia su estado local. Los modos `0700`/`0600` expresan el aislamiento por propietario; no hay API `chmod`, ACL de compartición o grupos.

### Planificación y publicación de archivos

| Método y ruta | Solicitud | Respuesta correcta | Errores específicos | Reintento e idempotencia |
|---|---|---|---|---|
| `POST /files/upload/plan` | `{path,size,sha256,handle?}`. Hash del archivo completo obligatorio. | `200 UploadSession`, descrita abajo. Reserva la ruta, sin publicar archivo nuevo. | `404` padre; `409` reserva/directorio; `410` handle; `413` demasiados bloques; `503` sin DN; `507` ocupación. | Una repetición crea otra reserva solo si no hay una activa; no es idempotente. Conservar el `upload_id`. |
| `POST /files/upload/renew` | `{upload_id}`. | `200 UploadSession` con nueva `expires_at` y capabilities. Renueva también el handle asociado. | `410` ausente, ajeno o vencido. | Renovar antes de vencer. Sustituir tokens anteriores por los nuevos. |
| `POST /files/upload/commit` | `{upload_id,sha256,blocks:[{index,block_id,size,stored_on}]}`. | `200 {status:"publicado",path,replicas_ok}`. | `409` cantidad, hash, tamaño, orden o copias inválidas; `410` lease; `503` fallo de quorum. | Repetir mismo ID y SHA tras pérdida de respuesta: `200 {...,idempotent:true}` si ya estaba confirmado. No cambiar contenido bajo un ID. |
| `POST /files/upload/abort` | `{upload_id}`. | `204`. Los bytes sueltos se recolectan después. | `403` subida de otro usuario. | Idempotente: ID ausente también `204`; no despublica un commit previo. |
| `GET /files/download/plan?path=...&handle=...` | `handle` opcional. | `200 {path,size,sha256,block_size,blocks:[{index,block_id,size,targets,replicas}]}`. Cada target incluye `node,url,token`. | `404`; `410` handle; `503` bloque sin copias vivas. | Pedir plan nuevo cuando venzan las capabilities. Con handle conserva versión abierta y renueva su lease. |

`stored_on` enviado en el commit es informativo: el ControlNode reconstruye las ubicaciones verificadas desde los destinos del plan y ejecuta HEAD contra cada uno. No publica un bloque basándose únicamente en la afirmación del cliente. Cada destino planificado debe confirmar tamaño y hash correctos. Un plan degradado contiene menos destinos; sus copias planificadas también deben existir.

Ejemplo abreviado de `UploadSession`:

```json
{
  "upload_id": "identificador-aleatorio",
  "owner": "andres",
  "path": "/demo/a.bin",
  "size": 16777217,
  "sha256": "HASH_SHA256_COMPLETO_DE_64_HEX",
  "block_size": 8388608,
  "n_blocks": 3,
  "expires_at": "2026-10-24T18:00:00Z",
  "blocks": [
    {"index": 0, "targets": [
      {"node":"dn-01","url":"https://dn1:8001","token":"CAPABILITY"},
      {"node":"dn-02","url":"https://dn2:8001","token":"CAPABILITY"}
    ]}
  ]
}
```

La lista del ejemplo está abreviada y los hashes/tokens son marcadores, no valores utilizables. La fecha se incluye solo para mostrar formato. `expires_at` sustituye el campo histórico `lease_expires_at`; el cliente de esta entrega utiliza el contrato nuevo. `url` sustituye `host`/`port` como dirección autoritativa y permite conservar esquema HTTPS.

### Handles y acceso por rangos

| Método y ruta | Solicitud | Respuesta correcta | Errores específicos | Reintento e idempotencia |
|---|---|---|---|---|
| `POST /files/open` | `{path,mode:"r"|"w"}`; modo por defecto `r`. | `200 {handle,owner,path,mode,expires_at,file?}`. | `400` modo; `404` lectura ausente/padre; `409` directorio o escritor activo. | No es idempotente: puede crear otro handle de lectura. Conservar el ID. |
| `POST /files/lock` | `{path}`. | Igual que `open` con `mode:"w"`. | `409` reserva activa. | Un segundo lock devuelve conflicto hasta cerrar/vencer el anterior. |
| `POST /files/renew` | `{handle}`. | `200 Handle` con expiración renovada. | `410` inválido, ajeno o vencido. | Repetible mientras esté vigente. |
| `POST /files/close` | `{handle}`. | `204`. | `403` ajeno; `409` subida activa asociada. | Idempotente si no existe. No confirma una subida incompleta. |
| `GET /cluster/status` | Sesión; sin cuerpo. | `200 {nodes:[{node_id,url,capacity,used,alive,verified_blocks}],files,raft,replication_factor}`. `files` cuenta archivos del usuario. | `503` sin autoridad. | GET repetible. Expone información de topología a usuarios autenticados. |

Las primitivas `read` y `write` son composición de estos contratos. `read` calcula los bloques que intersectan el rango y hace GET con `Range` directamente al DN. `write` genera una versión local por copia sobre escritura y usa plan/PUT/commit con el handle del escritor. No existen endpoints que reciban todo el contenido de un archivo en el ControlNode.

## 2. Cliente ↔ DataNode y DataNode ↔ DataNode

### Bloques

| Método y ruta | Consumidor y auth | Solicitud | Respuesta | Errores e idempotencia |
|---|---|---|---|---|
| `PUT /blocks/{sha256}` | Cliente: capability `block-write`. Otro DN: clave interna. | Binario, `Content-Type: application/octet-stream`. Máximo 64 MiB y límite de la capability. Forward opcional, abajo. | `201` nuevo o `200` existente, `{block_id,size,stored_on:[node_id]}`. | `400` ID; `401` auth; `409` hash; `413` tamaño; fallo de persistencia. Repetir mismo contenido es idempotente; también revalida y completa forwarding. |
| `GET /blocks/{sha256}` | Cliente: capability `block-read` exacta para hash y nodo. Otro nodo: clave interna. | `Range: bytes=inicio-fin` opcional. | `200` binario o `206` parcial. `Content-Type: application/octet-stream`. | `401`, `404`, `416` rango, `500` corrupción. Probar otra réplica ante fallo; no aceptar bytes sin verificar. |
| `HEAD /blocks/{sha256}` | Capability de lectura o clave interna. Usado por CN al confirmar. | Sin cuerpo. | `200`, sin cuerpo; `Content-Length` del texto claro, `X-Block-SHA256`, `ETag`. Descifra y verifica el bloque entero. | `404` o `500` corrupción. Repetible; no transfiere los bytes por el control. |
| `DELETE /blocks/{sha256}` | Solo nodo con clave interna. | Sin cuerpo. | `204`, incluso si no existe. | `401`; error de almacenamiento. Idempotente. Solo ejecutar después de comprobar referencias y copias. |
| `POST /blocks/{sha256}/replicate` | Solo CN/nodo con clave interna. | `{target:{node,url}}`. | `200 {block_id,size,stored_on:[origen,destino]}` después de confirmar recepción. | `404` origen; `500` corrupción; `502` destino. Repetible, porque el destino recibe PUT por hash. |
| `GET /health` | Público. | Sin cuerpo. | `200 {status:"ok",node_id}`. | El proceso puede estar sano sin estar registrado en el líder. |
| `GET /inventory` | Solo CN con clave interna. | Sin cuerpo. | `200 {node_id,blocks:[{block_id,size}],used,capacity}`. | `401`; corrupción se excluye del inventario y se registra. Consulta costosa: valida contenido almacenado. |

`X-Forward-To` lleva **JSON** con los destinos autorizados; cambia el formato antiguo de URLs separadas por comas:

```json
[{"node":"dn-02","url":"https://dn2:8001","token":"CAPABILITY_DEL_DESTINO"}]
```

El cliente toma cada objeto del plan y lo incluye en esa cabecera. El primer DN persiste el bloque y lo propaga; `stored_on` solo contiene IDs cuya recepción confirmó. Una respuesta de PUT con una sola copia no acredita por sí misma que un plan de dos destinos esté listo para commit. La reparación puede usar autenticación interna en lugar de capabilities de cliente.

Las réplicas cifran independientemente con nonces aleatorios. El formato físico tiene prefijo de versión, nonce y ciphertext autenticado; no es el binario que el cliente descarga. El contenido se descifra y valida antes de servir rangos. La unidad de memoria es un bloque por solicitud, con posibles buffers adicionales del cifrado; no se carga el archivo entero.

## 3. DataNode ↔ ControlNode

Todos estos endpoints usan `X-Cluster-Key`, son atendidos por el líder y reciben información de nodos autenticados. Tras un failover la topología viva se reconstruye por registro/heartbeat/inventario; los archivos publicados y sus referencias están en el estado persistente.

| Método y ruta | Solicitud | Respuesta | Fallos, reintento e idempotencia |
|---|---|---|---|
| `POST /cluster/register` | `{node_id,url,capacity,used}`. URL HTTPS sin credenciales ni path; capacidad positiva. | `201 {status:"registered",node_id}`. | `400` registro; `401`; `503`. Upsert repetible para mismo nodo. |
| `POST /cluster/heartbeat` | Mismo formato que registro. | `201 {status:"registered",node_id}`; refresca señal de vida. | Repetible cada 3 s. Tras tres intervalos sin señal se excluye de planes. No se interpreta como prueba de integridad de todos los bloques. |
| `POST /cluster/blockreport` | `{node_id,blocks:[{block_id,size}]}` de bloques verificados. | `200 {accepted:cantidad}`. | `404` registrar primero; `401`; `503`. Reemplaza el inventario de ese nodo; emitir cada 30 s. |

El DataNode intenta los controles configurados. Si ninguno acepta, conserva sus bloques y reintenta en el siguiente ciclo. El líder también puede consultar `/inventory` y ordenar la reparación directamente; no es necesario poner toda orden en la respuesta del heartbeat, como se proponía originalmente.

## 4. ControlNode ↔ ControlNode

La comunicación autoritativa ocurre en el puerto `RAFT_ADDR`/`RAFT_ADVERTISE` configurado para cada nodo. La biblioteca administra votos, AppendEntries, términos y snapshots. TLS mutuo valida que el par pertenece a la CA del clúster. No se deben enviar comandos de metadatos inventados a los endpoints diagnósticos esperando replicarlos.

| Método y ruta REST conservada | Auth y consumidor | Respuesta | Semántica |
|---|---|---|---|
| `POST /cluster/replicate` | Clave interna; herramienta/administrador del clúster. | `200 {protocol,role,term,note}` en líder; `503` en seguidor. | Diagnóstico. El registro real se replica por transporte Raft. Repetir no modifica estado. |
| `POST /cluster/election` | Igual. | Igual. | Diagnóstico. No fuerza candidatura ni permite proclamarse líder por petición HTTP. |

La membresía de referencia de los tres controles se configura antes del primer arranque. No se ofrece una API de administración de membresía arbitraria; cambiar IDs o reinicializar discos exige seguir el procedimiento de recuperación y conservar una mayoría del estado confirmado.

## 5. Ejemplos de uso

Se usan variables locales sin secretos reales. Los ejemplos de shell asumen que la CA y el usuario fueron preparados según el README. No usar `curl -k` para demostrar validación TLS.

```bash
# Ver qué CN anuncia el liderazgo. Comprobar también los otros controles.
curl --cacert .secrets/ca.crt https://localhost:8000/health

# Autenticación: guardar la respuesta de forma privada, no publicarla.
curl --cacert .secrets/ca.crt -H 'Content-Type: application/json' \
  -d '{"username":"USUARIO","password":"CONTRASENA_LOCAL"}' \
  https://localhost:8000/auth/login

# TOKEN se obtiene del login; no incluir valores reales en el repositorio.
curl --cacert .secrets/ca.crt -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' -d '{"path":"/demo"}' \
  https://localhost:8000/fs/mkdir

curl --cacert .secrets/ca.crt -H "Authorization: Bearer $TOKEN" \
  'https://localhost:8000/fs/ls?path=/demo'

# CAPABILITY procede de targets/replicas del plan y corresponde a este DN/hash.
curl --cacert .secrets/ca.crt -H "Authorization: Bearer $CAPABILITY" \
  -H 'Range: bytes=0-1023' \
  "https://DIRECCION_DN:8001/blocks/$BLOCK_ID" --output rango.bin
```

La CLI evita manejar manualmente capabilities y forwarding:

```bash
python3 client/dfsha.py --control https://localhost:8000,https://localhost:8010,https://localhost:8020 login USUARIO
python3 client/dfsha.py mkdir /demo
python3 client/dfsha.py put archivo.bin /demo/archivo.bin --workers 4
python3 client/dfsha.py stat /demo/archivo.bin
python3 client/dfsha.py read /demo/archivo.bin 100 1024 --output rango.bin
python3 client/dfsha.py get /demo/archivo.bin descargado.bin
```

La lista de puertos de host depende del Compose o despliegue en uso; el README es la fuente operativa. En JSON/API se usan los `url` realmente anunciados, que deben ser alcanzables desde el cliente.

## 6. Política de fallos

Al verificar un commit el ControlNode envía `X-Block-Owner` en su HEAD interno. El DN exige que esa cuenta haya presentado los bytes mediante PUT autenticado y verificado. El registro privado `.owners` persiste esta prueba de posesión por bloque/cuenta. Un hash conocido y una copia existente no permiten atribuirse datos de otra cuenta. Esta cabecera exige autenticación interna y no modifica los permisos del GET del cliente.

Un GET de bloque fallido o corrupto pasa a otra réplica del plan. Un PUT del mismo hash admite reintentos crecientes; la CLI usa los intervalos 1, 2 y 4 s para fallos de red. Cuando vence la reserva o autorización se renueva antes de continuar o se solicita un plan nuevo. No se deben reintentar indefinidamente `401`, errores de validación o `507` sin corregir su causa.

Una respuesta `503` después de enviar una mutación no significa necesariamente que esta no se aplicó. Se conserva el ID de operación: commit es repetible por `upload_id`, abort/close son idempotentes, y operaciones como mkdir/mv requieren consultar el resultado antes de crear una intención nueva. Sin mayoría no hay publicación autoritativa. Los bloques ya escritos pueden permanecer cifrados y huérfanos hasta la limpieza posterior.
