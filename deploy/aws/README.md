# Despliegue en AWS Academy

Guía para correr DFSha en varias máquinas virtuales, según la infraestructura del Hito 1 (sección 5). En el Hito 2 hay un solo ControlNode activo; los ControlNodes de `vm-02` y `vm-03` se agregan en el Hito 3.

| VM | Qué corre | `COMPOSE_PROFILES` |
|---|---|---|
| vm-01 | ControlNode `cn-01` + DataNode `dn-01` | `controlnode,datanode` |
| vm-02 | DataNode `dn-02` | `datanode` |
| vm-03 | DataNode `dn-03` | `datanode` |
| vm-04 | DataNode `dn-04`, apagada hasta la demostración de escalado | `datanode` |

## 1. Crear las VMs

- Instancias EC2 `t3.small` con Ubuntu 22.04, todas en la misma VPC y subred.
- Un volumen EBS de 20 GB por VM, montado en `/srv/dfsha/blocks`.
- Un grupo de seguridad con estas reglas de entrada:

| Puerto | Origen | Para qué |
|---|---|---|
| 22 | Tu IP | SSH |
| 8000 | La VPC y la IP del cliente | API del ControlNode |
| 8001 | La VPC y la IP del cliente | API del DataNode: el cliente sube y baja bloques directamente de los DataNodes |

## 2. Preparar cada VM

```bash
sudo apt-get update && sudo apt-get install -y docker.io docker-compose-v2 git
sudo usermod -aG docker "$USER" && newgrp docker
git clone git@github.com:AndresVelezR/DFSha---Distributed-Systems.git dfsha
cd dfsha/deploy/aws
cp .env.example .env
```

Edita `.env` con los valores de esa VM:

| Variable | vm-01 | vm-02 | vm-03 |
|---|---|---|---|
| `COMPOSE_PROFILES` | `controlnode,datanode` | `datanode` | `datanode` |
| `CLUSTER_KEY` | la misma clave en todas | igual | igual |
| `CONTROLNODE_HOST` | IP privada de vm-01 | IP privada de vm-01 | IP privada de vm-01 |
| `DATANODE_ID` | `dn-01` | `dn-02` | `dn-03` |
| `ADVERTISE_HOST` | IP de vm-01 | IP de vm-02 | IP de vm-03 |

`ADVERTISE_HOST` es la dirección que el ControlNode le entrega al cliente para llegar a ese DataNode. Si el cliente corre dentro de la VPC, usa la IP privada; si corre desde un portátil, usa la IP pública de la VM.

## 3. Levantar

Primero vm-01 y después las demás. En cada una:

```bash
docker compose up -d --build
```

Desde cualquier máquina con acceso al puerto 8000:

```bash
curl http://<ip-de-vm-01>:8000/health     # alive_datanodes debe ser 3
```

## 4. Usar el cliente

El cliente solo necesita Python 3, sin dependencias:

```bash
python3 client/dfsha.py --control http://<ip-de-vm-01>:8000 login demo demo
python3 client/dfsha.py --control http://<ip-de-vm-01>:8000 mkdir /demo
python3 client/dfsha.py --control http://<ip-de-vm-01>:8000 put archivo.bin /demo/archivo.bin
python3 client/dfsha.py --control http://<ip-de-vm-01>:8000 get /demo/archivo.bin copia.bin
```

Para no repetir `--control`: `export DFSHA_CONTROL=http://<ip-de-vm-01>:8000`.

## 5. Demostraciones

- **Tolerancia a fallos:** apaga vm-02 desde la consola de AWS, espera 10 segundos y vuelve a descargar el archivo. `stat` muestra `replicas_ok: 1` para los archivos que tenían una copia en `dn-02`.
- **Escalado:** enciende vm-04, configúrala con `DATANODE_ID=dn-04` y ejecuta `docker compose up -d --build`. `/health` pasa a mostrar 4 DataNodes vivos sin reiniciar nada, y las subidas nuevas empiezan a usar `dn-04`.

## Verificación sin AWS

La misma configuración se probó en una sola máquina con tres proyectos de Docker Compose separados (`-p vm01`, `-p vm02`, `-p vm03`). Cada DataNode usó un puerto distinto (`DATANODE_PORT` 8001, 8002 y 8003), y `ADVERTISE_HOST` y `CONTROLNODE_HOST` apuntaron a la IP de la máquina. Así los nodos no comparten red de Docker y se comunican solo por IP y puertos publicados, como entre VMs:

```bash
docker compose -p vm01 --env-file vm01.env up -d --build
docker compose -p vm02 --env-file vm02.env up -d --build
docker compose -p vm03 --env-file vm03.env up -d --build
```
