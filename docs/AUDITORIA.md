# Auditoría del punto de partida — DFSha

Fecha: 24 de octubre de 2026. Este documento registra el estado **del ZIP y de la versión posterior del Hito 2 recuperada de GitHub**, antes de las correcciones finales. La base utilizada para continuar es el repositorio, más avanzado que el ZIP. No describe el estado actual del código una vez implementados los cambios; para ello se consultan la matriz de trazabilidad y el informe final.

## Material revisado y autoridad

- Enunciado oficial: `ST0263-SI3007-26-22-Proyecto1-DFS.pdf`, siete páginas. Se revisó el contenido completo extraído en extracción local de texto (intermedio no versionado).
- Especificación original completa: `docs/hito-1/hito-1-especificacion.pdf`, dieciséis páginas, revisada mediante extracción local de texto (intermedio no versionado). Fue localizada al incorporar el contenido de GitHub; la primera inspección del ZIP no la contenía.
- ZIP: `cmd/controlnode/main.go`, `cmd/datanode/main.go`, `client/dfsha.py`, los tres Dockerfiles, Compose y documentos `HITO2.md` y `README.hito2.md`.
- Repositorio recuperado: módulos `cmd/controlnode/`, `cmd/datanode/`, `client/dfsha.py`, pruebas Go/Python, `docker/`, `deploy/aws/`, README, script de demostración e informe `docs/hito-2/hito-2-arquitectura-y-comunicaciones.tex`. El historial incorporado incluye `c1b8295` y `f957ce6`; la integración local quedó en `4ea381e` antes del desarrollo final.
- Texto suministrado por el usuario: «Proyecto DFSha — Continuación y finalización de un sistema de archivos distribuido». Se utilizó como descripción del alcance solicitado y de las decisiones previas que el equipo desea conservar.

Se distinguen los requisitos obligatorios del enunciado de sus ejemplos y sugerencias. HDFS, gRPC, Kafka, RabbitMQ y Python/FastAPI/Flask no son tecnologías obligatorias del PDF. C/S, Go, Python, REST, tres ControlNodes y los parámetros numéricos son decisiones explícitas de la especificación del Hito 1.

La auditoría inicial es de lectura de código y documentos; no constituye evidencia de que el Hito 2 haya pasado pruebas de ejecución.

## Requisitos oficiales

| Identificador | Requisito del PDF | Página |
|---|---|---|
| RF1 | Gestión de un sistema de archivos jerárquico: `ls`, `cd`, `mkdir`, `rmdir`, `rm`, etc. | 1 |
| RF2 | Transferencia de archivos: `send()` y `receive()`. | 1 |
| RF3 | Acceso a archivos: `open()`, `close()`, `read()`, `write()`, `lock()`, etc. | 1 |
| RNF1 | Escalabilidad en usuarios, archivos y tamaño de archivos. | 1 |
| RNF2 | Alta disponibilidad y redundancia para evitar caída del servicio o pérdida de datos. | 1 |
| RNF3 | Resolver consistencia de los datos derivada de la replicación. | 1 |
| RNF4 | Distribución de archivos entre nodos y de cada archivo entre varios nodos. | 2 |
| RNF5 | Concurrencia en acceso a archivos y datos dentro de un archivo. | 2 |
| RNF6 | Cifrado de comunicaciones y almacenamiento, control de acceso por usuario/grupo y seguridad entre nodos. | 2 |
| RNF7 | Transparencia de acceso/localización con acceso dinámico; nunca direccionamiento estático de los archivos. | 2 |
| RNF8 | Posibles aclaraciones o modificaciones futuras; el PDF no concreta requisitos adicionales. | 2 |

El proyecto también debe definir formalmente el servicio y su arquitectura asignada (p. 4); ejecutarse sobre Internet y especificar las cinco relaciones de comunicación (pp. 4–5); utilizar máquinas virtuales IaaS de AWS Academy o GCP académico, con nodos nativos o Docker y una API por nodo (p. 5); entregar informe PDF/Word, código documentado en GitHub y video de 10–15 minutos (pp. 5–7).

## Correspondencia entre documentación y código inicial

| Área | Evidencia inicial | Evaluación inicial |
|---|---|---|
| Arquitectura | Go para ControlNode/DataNode; Python para CLI; transferencia directa cliente–DataNode. | Coincide con la arquitectura C/S documentada. |
| Namespace | Handlers separados en `handlers_filesystem.go`; `ls`, `mkdir`, `rmdir`, `rm`, `mv`, `stat`; mapa global. | Parcial: no `cd`, árboles por usuario ni permisos. `stat` solo admite archivos. |
| Transferencia | Planificación, PUT de bloques, commit, GET y reconstrucción. | Existe implementación; requiere pruebas y controles de integridad de la publicación. |
| Acceso | DataNode usa `http.ServeContent`, que admite rangos HTTP. | Faltan API/CLI `open`, `close`, `read`, `write`, `lock`. |
| Particionamiento | Tamaño dinámico 8–64 MiB, cuatro bloques objetivo por nodo; SHA-256 por bloque. | Implementado en código, sin pruebas iniciales aportadas. |
| Distribución | `planner.go`: menor ocupación y carga virtual, RF limitado por N−1, comprobación agregada de espacio. | Avance respecto del ZIP. Falta validar capacidad y umbral por destino durante cada asignación. |
| Replicación | `handlers.go` y `peer_client.go`: propagación y réplica manual. | GitHub corrige el retorno temprano del ZIP: un bloque existente sí se reenvía. Falta reparación y comprobación fuerte del commit. |
| Seguimiento de nodos | Configuración separada; heartbeat 3 s y timeout 3×3=9 s. | GitHub corrige el timeout fijo de 10 s del ZIP; falta reparación y rebalanceo automáticos. |
| Inventario | `/cluster/blockreport` acepta JSON y devuelve el número de IDs. | Solo contrato: no se emite periódicamente ni reconcilia ubicaciones. |
| Control distribuido | `/cluster/replicate` y `/cluster/election`. | Ambos devuelven 501. Solo hay un ControlNode en Compose. |
| Persistencia | Bloques en volúmenes Docker; metadatos en mapas Go. | Reiniciar el ControlNode pierde namespace y archivos publicados. |
| Seguridad | Un usuario y token constantes; clave del clúster por entorno con valor por defecto. | Autenticación de demostración. Sin usuarios separados, expiración, HTTPS o cifrado en reposo. |
| Recursos del cliente | GitHub lee un bloque por worker y escribe descargas por offset en `.part`. | Corrige la memoria proporcional al archivo del ZIP; preservable. |
| Docker y despliegue | `docker/`, Compose 1 CN + 3 DN y cuarto DN opcional; `deploy/aws/` para varias VMs. | Infraestructura existente que debe evolucionar hacia 3 CN, TLS y secretos. |
| Evidencias | Pruebas unitarias Go/Python, demo multibloque con fallo de DN y alta de dn4. | Existen pruebas en GitHub ausentes del ZIP; deben ampliarse y ejecutarse de nuevo sobre la entrega. |

## Defectos concretos que requieren corrección

1. `uploadCommit` confía en los IDs, tamaños y ubicaciones enviados por el cliente. No comprueba los bloques contra el plan, número de réplicas, tamaños esperados o existencia real. Puede publicar metadatos de un archivo incompleto.
2. GitHub incorpora `hasActiveReservation`, ausente en el ZIP, para evitar subidas simultáneas a una ruta. Todavía debe persistirse y replicarse la reserva y coordinarla con `open`, `lock`, movimientos y borrados.
3. Las reservas vencen al intentar confirmar, pero no hay limpieza periódica. `rm` y `abort` no recolectan bloques huérfanos.
4. Los tokens globales permiten acceder directamente a cualquier bloque conociendo su SHA-256. El servidor no verifica propiedad por usuario ni método/ruta del bloque autorizado.
5. El DataNode valida longitud del ID, sin exigir hexadecimal SHA-256. La ruta del endpoint de réplica tampoco aplica las mismas validaciones.
6. `PUT` utiliza un temporal fijo `{id}.tmp`; dos subidas simultáneas del mismo bloque pueden interferirse. La ruta existente sí completa forwarding en GitHub, pero todavía no recalcula el hash del archivo guardado.
7. El almacenamiento se hace en texto claro y todo el tráfico usa HTTP. Los secretos de demostración aparecen en Compose y documentos.
8. El DataNode usa `syscall.Statfs`, específico de plataformas Unix, que impide compilarlo directamente en Windows. Debe resolverse la portabilidad o explicitar un entorno Linux verificable.
9. GitHub corrige la ocupación respecto del ZIP: `usage` suma bloques propios. La capacidad sigue siendo la del volumen, por lo que varios nodos en el mismo disco pueden sobreestimar capacidad agregada; una cuota por nodo vuelve la demo local más representativa.
10. No hay bloqueo de quorum, consenso, recuperación de líder ni registro persistente del control. Elegir un líder por mayoría sin un protocolo completo no basta para demostrar la conservación de escrituras confirmadas.

## Decisiones que se deben conservar o justificar

Se conserva el modelo por bloques, C/S Maestro–Trabajador, Go/Python, REST/JSON y binario directo, separación de planos, topología objetivo 3 CN + 3 DN, SHA-256, RF 2, rango de bloques 8–64 MiB y paralelismo cuatro. Cualquier sustitución del algoritmo de elección propuesto por un protocolo de consenso debe documentar el motivo, persistencia, mayoría necesaria y límites de fallos.

El objetivo de disponibilidad del 99,5 % es un objetivo de diseño. Una prueba funcional de failover no mide ese porcentaje ni lo convierte en un SLA acreditado. El despliegue en tres VMs, el video y las pruebas de Internet deben identificarse como pendientes si el entorno de trabajo no permite realizarlos.

## Ajustes necesarios de la especificación original

- La elección del matón con mayoría no especifica términos persistentes, votación por término, conciliación de registros ni reglas de commit. Esa descripción no prueba la conservación de operaciones confirmadas. Se justifica sustituir únicamente el mecanismo de consenso por Raft y documentar el cambio de protocolo interno.
- Las lecturas en seguidores propuestas por el Hito 1 podrían devolver metadatos atrasados. La entrega sirve lecturas autoritativas en el líder después de comprobar mayoría y aplicar el registro; seguidores indican el líder al cliente.
- La fórmula `A_archivo = A_bloque^M` del Hito 1 usa un supuesto adicional de independencia entre disponibilidades de bloques, que no se sostiene cuando comparten nodos y dominios de fallo. Se conserva RF 2 como configuración de referencia sin afirmar un SLA medido.
- La cota clásica de Graham sobre máquinas idénticas no constituye una garantía formal del balance de porcentajes de ocupación con capacidades heterogéneas, restricciones de réplica y nodos que entran/salen. Se mantiene la heurística como decisión práctica, evaluada con métricas.
- Se conserva la escritura degradada prevista en la página 8 del Hito 1. Cuando solo existe una copia válida, se pierde la tolerancia a otra caída de datos hasta completar la reparación.
- El Hito 1 permite conceder permisos explícitos sobre directorios. La entrega limita el acceso al árbol privado del propietario; compartir mediante ACL no se presenta como implementado. El RNF6 del enunciado admite control por usuario o grupo.
