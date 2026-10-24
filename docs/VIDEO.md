# Guion de sustentación — DFSha

Duración objetivo: **13 minutos y 30 segundos**, dentro de los 10–15 minutos exigidos. Presentan Andrés Vélez Rendón y Jose Luis Restrepo. Este archivo es un guion; no se presenta como un video ya grabado.

## Preparación antes de grabar

1. Seguir el README desde un entorno limpio y conservar los volúmenes. Preparar las credenciales sin mostrarlas en pantalla. No abrir `.secrets`, el archivo de sesión ni capturar tokens en el video.
2. Levantar el clúster y esperar tres CN y tres DN operativos. Dejar abiertas una terminal de cliente, una de administración de contenedores y los diagramas.
3. Preparar un binario de al menos 40 MiB para mostrar varios bloques. Guardarlo en `demo-files/original.bin` y obtener su SHA-256. Un archivo de texto de pocos bytes no demuestra particionamiento.
4. Ejecutar previamente las pruebas y conservar `docs/resultados/integration.json`. Si un escenario falla, resolverlo antes de grabar y mostrar la ejecución válida; no sustituir un fallo por una afirmación verbal.
5. Grabar Docker si está disponible o identificar explícitamente la demostración nativa con procesos independientes. Reservar el despliegue de tres VMs para cuando exista ejecución real en AWS Academy.

Los comandos siguientes usan Bash, la configuración Compose raíz y un clúster de demostración sin datos ajenos. En PowerShell se puede invocar la misma orden `docker compose ... client` sin el alias de función.

```bash
python3 scripts/setup.py
docker compose up -d --build
docker compose --profile tools run --rm client login demo

dfsha() { docker compose --profile tools run --rm client "$@"; }
mkdir -p demo-files
python3 -c 'import os; open("demo-files/original.bin","wb").write(os.urandom(40*1024*1024+17))'
sha256sum demo-files/original.bin
```

Si los secretos ya existen, no regenerarlos: conservar la configuración y usar el login. La función cliente trabaja en `/work`, donde está montado el repositorio. Las credenciales aleatorias iniciales se consultan de forma privada antes de grabar.

## Desarrollo de la presentación

| Tiempo | Responsable | Pantalla y acción | Explicación que debe darse |
|---|---|---|---|
| 00:00–00:50 | Andrés | Portada y objetivo. | Problema, Opción 1 asignada y autores. Se distribuyen bloques con varias copias, se conserva un namespace y se ofrecen operaciones de acceso. |
| 00:50–02:00 | Jose Luis | Diagramas 1 y 2, `docker compose ps`. | Separación de planos; tres controles y tres datos. Compose local demuestra procesos en red, tres EC2 separan dominios físicos de fallo. |
| 02:00–03:00 | Andrés | Login sin contraseña visible, `mkdir`, `cd`, `pwd`, `ls`. | Sesión expira y cada usuario tiene su propio árbol. El servidor decide permisos; `cd` se conserva en cliente. |
| 03:00–04:40 | Andrés | Subir binario, logs, `stat` y descargar. | Tamaño de bloque dinámico, workers y SHA-256. Mostrar IDs y ubicaciones de varios bloques en diferentes DataNodes. El ControlNode no transporta el archivo. |
| 04:40–05:40 | Jose Luis | Rango pequeño y handles. | Diferenciar transferencia completa de acceso. `read` descarga solo bloques/rangos relevantes. Un escritor reserva la ruta; otro escritor recibe conflicto. |
| 05:40–07:10 | Jose Luis | Detener un DN y descargar; reiniciarlo. | El cliente usa otra réplica. Heartbeat detecta caída y reparación viaja DN↔DN. Mostrar el estado degradado durante convergencia. |
| 07:10–08:40 | Andrés | Identificar y detener CN líder; volver a listar/descargar. | Raft necesita dos de tres; nuevo líder conserva lo confirmado. Reincorporar el nodo. Explicar el rechazo sin mayoría usando evidencia automatizada. |
| 08:40–09:40 | Jose Luis | Agregar `dn4` y revisar inventario/estado. | Registro dinámico; nuevas escrituras y balance gradual. Copiar y verificar antes de retirar una ubicación anterior. |
| 09:40–10:40 | Jose Luis | API y fragmento del formato cifrado o prueba de corrupción sanitizada. | HTTPS, claves internas, capabilities de bloques y AES-GCM. El token de sesión no permite leer un hash arbitrario directamente. |
| 10:40–12:20 | Andrés | JSON de resultados reales y tabla de métricas. | Pruebas funcionales, corrupción, reservas, reinicios y fallos. Comparar tamaños y workers sin prometer mejora universal. Mostrar fecha, TLS y número de procesos. |
| 12:20–13:30 | Ambos | Limitaciones y README. | Mayoría necesaria, riesgo de una sola copia, coste del estado completo, sin ACL compartidas, AWS pendiente si aplica y 99,5 % como objetivo. Mostrar dónde ejecutar y repetir pruebas. |

## Comandos y evidencias por escena

### Namespace, transferencia e integridad

```bash
dfsha mkdir /demo
dfsha cd /demo
dfsha pwd
dfsha put /work/demo-files/original.bin original.bin --workers 4
dfsha ls
dfsha stat original.bin
dfsha get original.bin /work/demo-files/descargado.bin --workers 4
sha256sum demo-files/original.bin demo-files/descargado.bin
```

Los hashes deben coincidir. En `stat`, enseñar `n_blocks`, `block_size`, `replicas_ok` y `blocks[].stored_on`. No basta enseñar que hay seis contenedores: hay que mostrar que los bloques de ese archivo ocupan nodos diferentes. Para evitar volver a mostrar un archivo anterior sin haber subido nada, utilizar una ruta nueva por grabación o limpiar solo los datos de la demo antes de comenzar.

### Rangos y exclusión de escritor

```bash
dfsha read /demo/original.bin 8388590 80 --output /work/demo-files/rango.bin
python3 -c 'from pathlib import Path; f=Path("demo-files/original.bin").open("rb"); f.seek(8388590); assert f.read(80)==Path("demo-files/rango.bin").read_bytes(); print("Rango correcto, cruza límite de bloque")'
dfsha open /demo/original.bin --mode r
dfsha lock /demo/original.bin
```

Anotar los handles emitidos. Intentar otro `lock` y mostrar `409`. Cerrar el handle escritor para continuar; no dejar una reserva activa que altere las escenas posteriores. Para mostrar `write`, preparar unos bytes en un archivo local, usar `write HANDLE ARCHIVO --offset N` y explicar que construye una versión completa por copia sobre escritura; no cambia los bloques en sitio.

```bash
dfsha close HANDLE_ESCRITOR
dfsha close HANDLE_LECTOR
```

### Caída y recuperación de DataNode

```bash
docker compose stop dn1
dfsha get /demo/original.bin /work/demo-files/sin-dn1.bin
sha256sum demo-files/original.bin demo-files/sin-dn1.bin
docker compose logs --tail 40 cn1 cn2 cn3
docker compose start dn1
```

El momento de detener el nodo importa: detener después de obtener un plan obliga a probar el fallback de una dirección que ya estaba anunciada. La prueba automatizada conserva el plan y verifica este caso. En la escena manual, mostrar el ID del DN usado en cada descarga y el estado del clúster; esperar la detección por heartbeat cuando se quiera demostrar que dejó de aparecer en planes nuevos.

### Caída del líder

```bash
curl --cacert .secrets/ca.crt https://localhost:8000/health
curl --cacert .secrets/ca.crt https://localhost:8010/health
curl --cacert .secrets/ca.crt https://localhost:8020/health
```

Identificar `role:"leader"` y su `node_id`. La correspondencia es `cn-01 → cn1`, `cn-02 → cn2`, `cn-03 → cn3`. Ejecutar `docker compose stop SERVICIO_LIDER` sustituyendo por ese servicio, esperar la nueva elección y después:

```bash
dfsha ls /demo
dfsha get /demo/original.bin /work/demo-files/sin-lider-anterior.bin
sha256sum demo-files/original.bin demo-files/sin-lider-anterior.bin
```

Reiniciar el servicio detenido con `docker compose start SERVICIO_LIDER` y mostrar su reincorporación. No afirmar «nunca falla»: sin una mayoría conectada el sistema debe rechazar operaciones autoritativas, como comprueba la batería de integración.

### Crecimiento y pruebas

```bash
docker compose --profile scale up -d dn4
docker compose ps
docker compose logs --tail 40 cn1 cn2 cn3

# La batería nativa usa un clúster temporal propio; no altera los volúmenes Docker.
python3 scripts/build.py
python3 scripts/integration_test.py
```

Abrir el archivo sanitizado `docs/resultados/integration.json`: `status`, `tls_verified`, `checks`, `benchmarks` y `storage`. Mostrar la evidencia efectiva de escalado y balance; la mera aparición del contenedor dn4 no demuestra movimiento de bloques. La integración puede tardar más que el espacio disponible de video; ejecutar antes y mostrar un fragmento junto con el reporte completo guardado, identificándolo como ejecución previa.

## Preguntas previsibles para la sustentación

| Pregunta | Respuesta comprobable |
|---|---|
| ¿Por qué tres controles? | Con tres se puede perder uno y mantener mayoría de dos. Con uno no hay failover; con dos no se mantiene mayoría al perder uno. |
| ¿Por qué cambió el matón? | Elegir coordinador no resuelve por sí solo el registro bajo partición. Se usó una biblioteca Raft persistente y se documentó la desviación. |
| ¿Qué sucede con una sola réplica? | Puede leerse mientras conserve contenido válido; no tolera otra pérdida de ese bloque antes de repararlo. |
| ¿Dónde se cifra? | En cada DN con AES-GCM; las copias pueden tener ciphertext distinto y el mismo SHA-256 del contenido. |
| ¿El control ve el archivo? | No. Recibe manifiestos y valida bloques por HEAD; el DN verifica contenido localmente. |
| ¿Se modifican bloques? | No. `write` crea una versión nueva y el commit cambia los metadatos de una vez. |
| ¿El 99,5 % está probado? | Es un objetivo del Hito 1; las pruebas funcionales de fallos no miden disponibilidad anual. |
| ¿Esto ya corrió en AWS? | Responder con la evidencia del despliegue real. Si solo hay prueba local, decirlo y mostrar la guía preparada. |
| ¿Qué garantiza un `200 /health`? | Que ese proceso responde. La autoridad de metadatos requiere líder y mayoría, comprobados en cada operación. |

No usar el video para mostrar secretos reales, presentar diagramas como si fueran pruebas ni afirmar despliegues que no se hayan ejecutado. Al finalizar, agregar al repositorio un enlace al video grabado o su ubicación de entrega académica.
