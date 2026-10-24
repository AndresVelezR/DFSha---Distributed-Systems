# Matriz de trazabilidad — DFSha

Fecha: 24 de octubre de 2026. La auditoría histórica está en [AUDITORIA.md](AUDITORIA.md). La especificación original completa del Hito 1 fue recuperada de GitHub y permanece en `docs/hito-1/hito-1-especificacion.pdf`.

«Verificado localmente» exige ejecución real; «implementado» identifica código disponible; «pendiente externo» requiere infraestructura o grabación. La evidencia integrada es [resultados/integration.json](resultados/integration.json), generada por `python scripts/integration_test.py` contra **3 ControlNodes y 3 DataNodes iniciales, ampliados a 4**, en procesos independientes con TLS verificado. El reporte conserva métricas y logs sanitizados. La batería acelera intervalos de heartbeat/mantenimiento; no certifica un SLA.

## 1. Requisitos del enunciado oficial

| Requisito y página | Implementación y archivos | Prueba / evidencia del reporte | Estado y límite |
|---|---|---|---|
| RF1, p. 1: namespace; ls, cd, mkdir, rmdir, rm, etc. | `cmd/controlnode/handlers_filesystem.go`; `client/dfsha.py`. Árbol privado, cwd local, archivos/directorios y mv de subárbol. | `authentication and filesystem isolation`; `move and remove published file`; unitarias de namespace/cliente. | Implementado y verificado localmente. Sin montaje POSIX ni ACL compartidas. |
| RF2, p. 1: send/receive | `handlers_upload.go`, `handlers_download.go`; CLI put/get y aliases. Transferencia directa con DN. | `multi-block upload, distribution, replication and SHA256`; métricas 1/24 MiB y hash. | Implementado y verificado localmente. Límites efectivos de manifiestos/metadatos declarados. |
| RF3, p. 1: open, close, read, write, lock | Handles persistentes, writer lease, lectura Range y write COW. | `range read, open/close, writer lock and copy-on-write`: rango entre bloques, exclusión y snapshot del lector. | Implementado y verificado localmente. Write puede transferir versión completa; no muta bloques. |
| RNF1, p. 1: escalabilidad | Registro dinámico, cuotas/capacidad de DN, planes y cliente por bloques. | `dynamic DataNode scale-out and rebalance`; dos usuarios; tamaños 1/24 MiB, workers 1/4. | Escalado DN verificado. Sin carga masiva de usuarios/archivos; estado completo por comando limita control. |
| RNF2, p. 1: redundancia y disponibilidad | `consensus.go`: Raft, BoltDB, snapshots; `maintenance.go`: reparación y retirada segura. | Líder caído y recuperado, DN caído, reinicio y `minority rejects writes and never publishes them`. | Verificado en fallos ensayados. Requiere dos CN; no recupera todas las copias perdidas. 99,5 % es objetivo. |
| RNF3, p. 1: consistencia | Registro por mayoría, VerifyLeader+Barrier, escritor único, commit con HEAD, inmutabilidad. | Publicación incompleta rechazada; writer lock, handles, COW, reinicios y minoría rechazada. | Implementado y verificado localmente; no tolerancia bizantina. |
| RNF4, p. 2: cada archivo entre varios nodos | Bloques dinámicos 8–64 MiB, SHA, carga virtual y réplicas distintas. | Archivo 24 MiB: 3 bloques, RF 2, 2 bloques por cada DN; ningún DN contiene todo ese archivo. | Verificado multibloque. Archivos menores de 8 MiB se distribuyen mediante réplicas de un bloque, según Hito 1 p. 6. |
| RNF5, p. 2: concurrencia | ThreadPoolExecutor y HTTP Go concurrente; un escritor, lectores sobre versiones inmutables. | Benchmarks 1/4 workers; rango, lector anterior y COW. | Verificado localmente. No se promete mejora universal o miles de clientes. |
| RNF6, p. 2: cifrado y acceso | `internal/common/common.go`, auth/middleware y DN storage: bcrypt, HMAC, TLS, Raft mTLS, AES-256-GCM. | TLS verificado, dos usuarios aislados, token sesión denegado por DN, corrupción y reparación; unitarias. | Implementado y verificado en pruebas registradas. Sin 2FA, grupos, ACL compartidas, KMS o rotación en línea. PDF admite control por usuario o grupo. |
| RNF7, p. 2: transparencia y localización dinámica | Direcciones/capabilities por plan, descubrimiento de líder y cwd local validado. | Cambio de CN, caída DN y alta dn4 conservando rutas lógicas. | Verificado localmente. Transparencia CLI/API; no montaje en SO. |
| RNF8, p. 2: ajustes | Informe §9 y auditoría documentan desviaciones. | Revisión frente al PDF de Hito 1. | Documentado; PDF no concreta otro requisito adicional. |

## 2. Condiciones y entregables oficiales

| Fuente | Condición | Entrega / evidencia | Estado |
|---|---|---|---|
| p. 4, §1 | Definición formal de servicio y problema. | Informe §§1–5, Hito 1 y esta matriz. | Documentado. |
| p. 4, §2 | Arquitectura de la opción asignada. | C/S distribuido, Maestro–Trabajador, Go/Python; diagramas 1/2. | Implementado y documentado. |
| pp. 4–5, §3 | Comunicación real sobre Internet. | Sockets HTTPS/mTLS locales; guía de VMs y red privada/VPN. | Local verificado; Internet/VMs requiere ejecución externa. |
| p. 5 | Cinco relaciones de comunicación. | [API.md](API.md) y [DIAGRAMAS.md](DIAGRAMAS.md). | Implementadas; CN↔CN Raft mTLS es desviación justificada de REST. |
| p. 5, §5 | AWS Academy o GCP académico con VMs IaaS. | [deploy/aws/README.md](../deploy/aws/README.md), compose y configuración por VM. | Preparado; despliegue académico real pendiente externo. |
| p. 5, §5 | Nodos nativos o Docker, API por nodo. | Binarios independientes, Dockerfiles, Compose con healthchecks/volúmenes. | Nativo verificado; imágenes Docker construidas y clúster 3 CN/3 DN levantado. Evidencia final de demo en README/resultados. |
| pp. 5, 7 | Informe PDF o Word. | [INFORME_FINAL.md](INFORME_FINAL.md), fuente editable completa. | PDF generado y revisado visualmente: output/pdf/DFSha-informe-final.pdf. |
| pp. 5, 7 | Código documentado en GitHub. | Código, dependencias, scripts y README. | Entrega versionada y publicada en el repositorio indicado; consultar el commit de entrega. |
| pp. 5, 7 | Video 10–15 min. | [VIDEO.md](VIDEO.md), guion 13 min 30 s. | Guion preparado; grabación y enlace pendientes externos. |

## 3. Decisiones del Hito 1 y ampliaciones solicitadas

| Decisión | Implementación final | Evidencia o desviación |
|---|---|---|
| 3 CN + 3 DN; cuarto DN opcional. | Configuración local/multihost y registro dinámico. | Integración 3 CN/3→4 DN; tres VMs requieren evidencia externa. |
| Bloques 8–64 MiB, k=4; RF 2 y RF≤N−1. | `BLOCK_SIZE_MIN_MB`, `BLOCK_SIZE_MAX_MB`, `BLOCKS_PER_NODE`, `REPLICATION_FACTOR`; `copiesPerBlock`. | Unitarias de planificación y binario multibloque. Unidades binarias explícitas. |
| Escritura degradada si faltan DN. | Plan reduce destinos al número posible; commit exige todas las copias del plan. | Conservada. Una copia no tolera otra pérdida antes de reparar; no se presenta como RF objetivo satisfecho. |
| DN heartbeat 3 s, caída 3 pérdidas, inventario 30 s. | `HEARTBEAT_MS=3000`, `HEARTBEAT_MISSES=3`, `REPORT_MS=30000`; pull de inventario por CN. | Valores por defecto documentados; integración usa tiempos acelerados. |
| Cuatro bloques concurrentes. | CLI `--workers`, cuatro por defecto. | Cuatro combinaciones tamaño/workers medidas en reporte real. |
| Lease de escritor 60 s. | `WRITE_LEASE_SECONDS=60`, renew de subida/handle y lock. | Exclusión de escritores, leases persistentes y publicación incompleta rechazada. |
| CN heartbeat 2 s y matón. | Timeout de referencia 2 s; votos/latidos de biblioteca Raft. | Raft sustituye matón para seguridad de registro. No equivale a un failover garantizado de 2 s. |
| Ocupación 90 %, rebalance 10 %. | `FULL_THRESHOLD_PERCENT`, `REBALANCE_THRESHOLD_PERCENT`, cuota por DN. | Admisión por destino; dn4 recibe bloques. Balance gradual. |
| Copiar antes de borrar; conservar copias válidas. | Copiar, HEAD, confirmar metadatos, GCGrace, revalidar otras copias antes DELETE. | Reparación y balance ensayados. Retirada física espera gracia; no prueba formal de particiones arbitrarias. |
| Consistencia y operaciones interrumpidas. | Estado Raft, expiración, commit idempotente, GC conservador. | Fallos/reinicios registrados. No equivalen a pérdida de discos. |
| Usuario/contraseña, permisos y HTTPS. | bcrypt, sesión 1 h, árbol 0700/0600, TLS y capabilities de bloques. | Dos usuarios y acceso directo denegado. Compartir directorios por ACL previsto en Hito 1 no implementado. |
| Cifrado de bloques. | AES-256-GCM, nonce por copia, hash plaintext, secretos externos. | Corrupción detectada, fallback y reparación. |
| REST en cinco relaciones. | Cuatro REST/HTTPS; CN↔CN transporte Raft mTLS. Endpoints históricos son diagnóstico. | Cambio explícito sustentado en informe y API. |
| RF3 y escritura inmutable. | open/close/read/write/lock; COW para offset. | Snapshot del lector, rango y COW verificados; coste posible de archivo completo. |
| Documentación y reproducibilidad. | README, entorno ejemplo, setup/build/test, API, informe, 8 diagramas y guion. | AWS real y grabación se entregan por separado. |

## 4. Reproducción y límites

```bash
python scripts/build.py
go test ./...
python -m unittest discover -s client -v
python scripts/integration_test.py
```

La integración usa un clúster temporal y credenciales propias, no los datos del Compose del usuario. `--output RUTA` cambia el reporte. Si una ejecución posterior falla, registrar y corregir el fallo; no editar resultados para convertirlos en éxito.

Las pruebas locales no acreditan fallos físicos de EBS, zonas de disponibilidad, conectividad de Internet, reloj malicioso, servidores internos hostiles ni disponibilidad anual. La prueba extendida resultados/integration-extended.json esperó renovación, vencimiento y gracia reales; verificó eliminación física de huérfanos y dos réplicas válidas después de la retirada. La batería final resultados/integration.json añade rechazo de apropiación de un bloque ajeno mediante hash conocido.
