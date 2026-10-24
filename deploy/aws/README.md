# Despliegue DFSha en tres EC2 de AWS Academy

Esta receta ejecuta **una instancia ControlNode y una DataNode por máquina**,
con discos y procesos independientes. El Compose raíz es el laboratorio local;
`deploy/aws/compose.yml` usa la red del host Linux y direcciones privadas reales.
El despliegue cloud debe ejecutarlo el estudiante en su cuenta Academy. No se
ha aprovisionado AWS ni se presentan resultados cloud como medidos.

## 1. Preparar las máquinas y la red

Crear tres EC2 Ubuntu 24.04 LTS, en la misma VPC, con direcciones privadas
estables. Ejemplo que debe sustituirse por las IP reales:

| Máquina | ControlNode | DataNode | IP privada |
|---|---|---|---|
| VM1 | cn-01 | dn-01 | 10.0.1.10 |
| VM2 | cn-02 | dn-02 | 10.0.1.11 |
| VM3 | cn-03 | dn-03 | 10.0.1.12 |
| VM4 opcional | — | dn-04 | 10.0.1.13 |

Como punto de partida de laboratorio, reservar 2 vCPU y 4 GiB RAM por máquina;
esto es una configuración sugerida, no una medición de capacidad. Comprobar
las cuotas y créditos de Academy. Separar el almacenamiento persistente del
ciclo de vida del contenedor, por ejemplo montando EBS en `/var/lib/dfsha`.
Cada DataNode anuncia por defecto una capacidad lógica de 1 GiB; ampliarla con
`CAPACITY_BYTES` solo si el disco realmente tiene espacio libre suficiente.

Crear un grupo de seguridad `dfsha-cluster`, asociarlo a todas las instancias
y configurar estas entradas:

| TCP | Origen permitido | Uso |
|---|---|---|
| 22 | IP pública /32 del administrador o bastión | SSH |
| 8000 | el propio grupo `dfsha-cluster` y rango VPN del cliente | API HTTPS ControlNode |
| 8001 | el propio grupo y rango VPN del cliente | Bloques HTTPS DataNode |
| 9000 | únicamente el propio grupo | Raft con TLS mutuo |

Permitir salida entre los miembros y salida HTTPS para instalar dependencias.
No abrir 8000, 8001 ni 9000 a `0.0.0.0/0`. El cliente debe alcanzar las IP
anunciadas de todos los DataNodes: usar una VPN de la VPC o ejecutar el cliente
en VM1 mediante SSH. Un único túnel al ControlNode no basta para transferir
bloques. La red privada de la VPC y el acceso administrativo por Internet
mantienen la arquitectura asignada de servicio entre servidores en red privada.
Consultar [grupos de seguridad EC2](https://docs.aws.amazon.com/AWSEC2/latest/UserGuide/ec2-security-groups.html).

## 2. Instalar Docker y obtener el código

En **cada** instancia instalar Docker Engine y el complemento Compose siguiendo
el [repositorio apt oficial de Docker](https://docs.docker.com/engine/install/ubuntu/).
Comprobar `sudo docker version` y `sudo docker compose version`. Los comandos
siguientes suponen que el usuario actual puede ejecutar Docker; agregarlo al
grupo `docker` concede privilegios equivalentes a root, o usar `sudo`.

```sh
sudo apt update
sudo apt install -y git python3
sudo mkdir -p /var/lib/dfsha/control /var/lib/dfsha/blocks
git clone https://github.com/AndresVelezR/DFSha---Distributed-Systems.git
cd DFSha---Distributed-Systems
```

Si se usa EBS, montar primero el volumen y luego crear esas carpetas, para no
escribir accidentalmente en el disco raíz. No formatear un volumen que contenga
datos existentes. EBS conserva los datos independientemente de una instancia
en ejecución; al eliminar una EC2 revisar la opción de eliminar sus volúmenes.
Ver [volúmenes EBS](https://docs.aws.amazon.com/ebs/latest/userguide/ebs-volumes.html).

## 3. Generar secretos una sola vez y distribuirlos

En una estación administrativa con el repositorio y Docker:

```sh
python3 scripts/setup.py --hosts 10.0.1.10,10.0.1.11,10.0.1.12,10.0.1.13
```

Este comando genera claves aleatorias, CA, certificado con SAN para todas las
IP y DNS indicadas y `users.json` con contraseñas bcrypt. En `credentials.txt`
queda la contraseña inicial de `demo`; consultarla localmente. También pueden
usarse `--native` con Go >=1.25 y `--password-file` para una contraseña privada.
No regenerar claves si existen datos: cambiar la clave de cifrado hace ilegibles
los bloques anteriores. Mantener una copia privada recuperable de los secretos.

Preparar un archivo temporal de distribución excluyendo la clave privada de
la CA y las contraseñas en texto claro:

```sh
umask 077
tar --exclude=ca.key --exclude=credentials.txt -czf /tmp/dfsha-runtime.tgz -C .secrets .
scp -i CLAVE_EC2.pem /tmp/dfsha-runtime.tgz ubuntu@IP_PUBLICA_VM1:/tmp/
# Repetir scp para VM2 y VM3 (y VM4 si se utiliza).
```

En cada VM, dentro del repositorio:

```sh
mkdir -p .secrets
chmod 700 .secrets
tar -xzf /tmp/dfsha-runtime.tgz -C .secrets
chmod 600 .secrets/*
rm /tmp/dfsha-runtime.tgz
```

Los archivos runtime requeridos son `ca.crt`, `cluster.crt`, `cluster.key`,
`cluster-key`, `signing-key`, `encryption-key` y `users.json`. El montaje es de
solo lectura. Los nodos comparten estas claves en este prototipo académico;
comprometer un miembro compromete el dominio de confianza del clúster.
Guardar `ca.key` fuera de las VM. Transferir secretos mediante SSH/SCP,
comprobar las huellas de los hosts y eliminarlos de las ubicaciones temporales.
No agregar `.secrets`, claves EC2 ni sesiones a Git.

## 4. Configurar y levantar cada pareja CN/DN

En VM1 copiar `deploy/aws/node.env.example` a `deploy/aws/node1.env` y cambiar
las tres IP de `CN_PEERS` y `CONTROL_URLS` por las reales. En VM2 y VM3 repetir
con `node2.env` y `node3.env`. **Los tres archivos deben contener la misma lista
CN_PEERS**. Solo cambian `CN_ID`, `DN_ID`, `PRIVATE_IP` y, opcionalmente, el disco:

```dotenv
# VM2, ejemplo
CN_ID=cn-02
DN_ID=dn-02
PRIVATE_IP=10.0.1.11
CN_PEERS=cn-01@10.0.1.10:9000@https://10.0.1.10:8000,cn-02@10.0.1.11:9000@https://10.0.1.11:8000,cn-03@10.0.1.12:9000@https://10.0.1.12:8000
CONTROL_URLS=https://10.0.1.10:8000,https://10.0.1.11:8000,https://10.0.1.12:8000
DATA_ROOT=/var/lib/dfsha
CAPACITY_BYTES=1073741824
REPLICATION_FACTOR=2
```

Ejecutar en VM1 (sustituir el archivo por el correspondiente en VM2 y VM3):

```sh
docker compose --env-file deploy/aws/node1.env -f deploy/aws/compose.yml config --quiet
docker compose --env-file deploy/aws/node1.env -f deploy/aws/compose.yml up -d --build
docker compose --env-file deploy/aws/node1.env -f deploy/aws/compose.yml ps
docker compose --env-file deploy/aws/node1.env -f deploy/aws/compose.yml logs --tail=60
```

Los ControlNodes requieren dos votos vivos de tres. Es normal que el primero
no atienda operaciones hasta iniciar otra VM. Raft elige automáticamente líder;
los DataNodes registran sus bloques y reintentan contra otro ControlNode.
Verificar los certificados, el grupo de seguridad y las IP si no hay mayoría.

Desde VM1, usando Python y las IP privadas:

```sh
export DFSHA_CONTROLS=https://10.0.1.10:8000,https://10.0.1.11:8000,https://10.0.1.12:8000
export DFSHA_CA="$PWD/.secrets/ca.crt"
python3 client/dfsha.py login demo
python3 client/dfsha.py mkdir /prueba
python3 -c 'import os; open("original.bin", "wb").write(os.urandom(40*1024*1024))'
python3 client/dfsha.py put original.bin /prueba/original.bin
python3 client/dfsha.py stat /prueba/original.bin
python3 client/dfsha.py get /prueba/original.bin copia.bin
sha256sum original.bin copia.bin
```

La contraseña se solicita sin eco. No configurar `DFSHA_INSECURE=true` en AWS.
Para un cliente externo copiar únicamente `ca.crt`; el cliente no necesita la
clave de cifrado ni las claves internas de los servidores.

## 5. Fallos, persistencia y cuarto nodo

Consultar `/health` por HTTPS con la CA para identificar el líder; cada CN
expone `role`, `leader`, y topología. Detener el servicio `control` del líder en
su VM, esperar la elección y repetir `ls`, `get` y SHA-256 desde VM1 o VPN.
Reiniciarlo con `docker compose ... start control`; conserva `raft.db` y snapshots
en `/var/lib/dfsha/control`. Detener solo un ControlNode a la vez mantiene la
mayoría. Detener dos debe impedir escrituras y lecturas fuertes por seguridad.

Detener `data` en una VM, descargar el archivo y comparar SHA-256. Tras tres
heartbeats perdidos (9 s por defecto) el líder deja de planificar en ese nodo;
la reparación copia bloques entre DataNodes disponibles. Reiniciar `data` y
consultar el estado hasta que reporte sus bloques. No borrar carpetas de datos
para simular una caída: `stop`/`start` conserva el estado real.

Para VM4 generar el certificado inicialmente con su IP, copiar los secretos,
usar `DN_ID=dn-04`, `PRIVATE_IP=10.0.1.13` y las mismas `CONTROL_URLS`. La variable
`CN_ID` puede conservar un valor de ejemplo porque no se iniciará `control`:

```sh
docker compose --env-file deploy/aws/node4.env -f deploy/aws/compose.yml up -d --build data
```

No agregar un cuarto votante a `CN_PEERS`: el escalado demostrado corresponde a
DataNodes. Verificar que `dn-04` aparece y recibe réplicas por reparación o
rebalanceo. Reiniciar una VM mantiene datos en el disco; eliminar el disco o las
claves no equivale a una falla tolerable. Respaldar por separado claves, logs
Raft/snapshots y bloques, y probar recuperación. Para una copia coordinada de
laboratorio detener los tres servicios antes de obtener snapshots de los discos.
La meta de disponibilidad 99,5 % no se demuestra con una sesión de pruebas.

## 6. Cierre del laboratorio

`docker compose ... down` quita contenedores y conserva las carpetas bind de
`DATA_ROOT`. Verificar las políticas de Academy para detener EC2 y conservar
discos; no terminar recursos ni borrar volúmenes que aún contengan evidencias.
Las instrucciones son reproducibles, pero no constituyen evidencia de una
ejecución AWS: registrar fecha, topología, salida de health, hashes y tiempos
cuando se realice efectivamente el despliegue.
