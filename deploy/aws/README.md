# Despliegue en AWS Academy

Tres máquinas virtuales, cada una con un ControlNode y un DataNode. Una cuarta
máquina opcional agrega solo un DataNode para mostrar el escalado. Los nodos se
hablan por las IP privadas de la VPC y el cliente corre dentro de la VM1.

| VM | Corre | Ejemplo de IP privada |
|---|---|---|
| vm1 | cn-01 y dn-01, y el cliente | 172.31.0.11 |
| vm2 | cn-02 y dn-02 | 172.31.0.12 |
| vm3 | cn-03 y dn-03 | 172.31.0.13 |
| vm4 (opcional) | dn-04 | 172.31.0.14 |

Las IP de la tabla son de ejemplo: en todos los pasos hay que usar las IP
privadas reales que asigne AWS.

## 1. Crear las máquinas

En la consola de EC2 del Learner Lab, lanzar tres instancias (cuatro si se va a
mostrar el escalado) con:

- Imagen: Ubuntu Server 24.04 LTS.
- Tipo: `t3.small` (2 GiB de memoria) o mayor.
- Par de claves: `vockey`. La clave privada (`labsuser.pem`) se descarga desde
  *AWS Details* en la página del laboratorio.
- Disco: 20 GiB.
- Todas en la misma VPC y subred (las que vienen por defecto sirven).

Crear un grupo de seguridad `dfsha` y asignarlo a todas las instancias. Reglas de
entrada:

| Puerto | Origen | Para qué |
|---|---|---|
| 22 | Mi IP | SSH desde el computador |
| 8000 | el grupo `dfsha` | API de los ControlNodes |
| 8001 | el grupo `dfsha` | Bloques de los DataNodes |
| 9000 | el grupo `dfsha` | Raft entre ControlNodes |

Poner el propio grupo como origen deja pasar el tráfico entre las VMs y nada más.
Ningún puerto del clúster queda abierto a Internet.

Anotar la IP privada de cada instancia. Las IP privadas no cambian al detener e
iniciar el laboratorio; las públicas sí, así que para SSH hay que mirar la IP
pública cada vez.

## 2. Generar los secretos (una sola vez, en el computador)

Desde la raíz del repositorio, con las IP privadas reales:

```bash
python3 scripts/setup.py --out .secrets-aws --hosts 172.31.0.11,172.31.0.12,172.31.0.13,172.31.0.14
```

Esto crea la CA, el certificado del clúster con esas IP, las claves y el usuario
`demo`. La contraseña queda en `.secrets-aws/credentials.txt`. Empaquetar lo que
necesitan las VMs, sin la clave de la CA ni las contraseñas:

```bash
tar --exclude=ca.key --exclude=credentials.txt -czf dfsha-secrets.tgz -C .secrets-aws .
```

Copiarlo a cada VM:

```bash
scp -i labsuser.pem dfsha-secrets.tgz ubuntu@IP_PUBLICA_VM1:~
```

Si después se quiere agregar una VM con otra IP, hay que volver a generar el
certificado con esa IP incluida; por eso conviene crear la vm4 desde el principio
aunque se encienda solo para la demostración.

## 3. Preparar cada VM

Entrar por SSH (`ssh -i labsuser.pem ubuntu@IP_PUBLICA`) y en cada una:

```bash
sudo apt-get update
sudo apt-get install -y docker.io docker-compose-v2 git python3
sudo usermod -aG docker ubuntu
exit
```

Volver a entrar para que el grupo `docker` tome efecto, y luego:

```bash
git clone https://github.com/AndresVelezR/DFSha---Distributed-Systems.git dfsha
cd dfsha
mkdir -m 700 .secrets
tar -xzf ~/dfsha-secrets.tgz -C .secrets
rm ~/dfsha-secrets.tgz
cp deploy/aws/vm.env.example deploy/aws/vm1.env
nano deploy/aws/vm1.env
```

En `vm1.env` poner las IP reales. En la vm2 el archivo se llama `vm2.env` y
cambian solo las tres primeras líneas (`cn-02`, `dn-02` y su IP), y así con las
demás. `CN_PEERS` y `CONTROL_URLS` son iguales en todas.

## 4. Levantar el clúster

En cada VM, con su archivo:

```bash
docker compose --env-file deploy/aws/vm1.env -f deploy/aws/compose.yml up -d --build
docker compose --env-file deploy/aws/vm1.env -f deploy/aws/compose.yml ps
```

La primera vez tarda unos minutos porque compila las imágenes. Un ControlNode
solo empieza a responder cuando hay dos de tres encendidos, porque Raft necesita
mayoría. Cuando las tres VMs están arriba, desde cualquiera:

```bash
curl --cacert .secrets/ca.crt https://172.31.0.11:8000/health
```

La respuesta dice el rol de ese nodo (`leader` o `follower`), quién es el líder y
cuántos DataNodes están vivos.

## 5. Usar el cliente desde la VM1

```bash
export DFSHA_CONTROLS=https://172.31.0.11:8000,https://172.31.0.12:8000,https://172.31.0.13:8000
export DFSHA_CA=$HOME/dfsha/.secrets/ca.crt
python3 client/dfsha.py login demo
python3 client/dfsha.py mkdir /prueba
head -c 100M /dev/urandom > original.bin
python3 client/dfsha.py put original.bin /prueba/original.bin
python3 client/dfsha.py stat /prueba/original.bin
python3 client/dfsha.py get /prueba/original.bin copia.bin
sha256sum original.bin copia.bin
```

La contraseña de `demo` está en `.secrets-aws/credentials.txt` en el computador.
`stat` muestra en qué DataNodes quedó cada bloque.

## 6. Pruebas de fallos y escalado

Las órdenes de esta sección se ejecutan en la VM indicada, desde `~/dfsha`.
Para abreviar:

```bash
dfsha-vm() { docker compose --env-file deploy/aws/vm$1.env -f deploy/aws/compose.yml "${@:2}"; }
```

- **Cae un DataNode.** En la vm2: `dfsha-vm 2 stop data`. Desde la VM1, `get`
  sigue funcionando con las otras copias. A los 9 s el líder lo da por caído y
  empieza a reponer copias; `stat` muestra cómo `replicas_ok` vuelve a 2.
  Para traerlo de vuelta: `dfsha-vm 2 start data`.
- **Cae el líder.** Ver quién es con `/health`, y en esa VM `dfsha-vm N stop control`.
  En unos segundos los otros dos eligen un líder nuevo y el cliente lo encuentra
  solo. También se puede detener la instancia completa desde la consola de EC2.
- **Sin mayoría.** Con dos ControlNodes detenidos, el que queda rechaza las
  operaciones con `503` en vez de arriesgar datos.
- **Escalado.** En la vm4, preparada igual que las demás con `vm4.env`, levantar
  solo el DataNode: `dfsha-vm 4 up -d --build data`. Aparece en
  `python3 client/dfsha.py status` y recibe las siguientes escrituras. Los bloques
  ya guardados se mueven hacia él si la diferencia de ocupación pasa del 10 %;
  para verlo con pocos datos se puede poner `REBALANCE_THRESHOLD_PERCENT=2` en los
  archivos `.env` y recrear los ControlNodes con `up -d control`.

`stop` y `start` conservan los datos. Metadatos y bloques quedan en
`/var/lib/dfsha` de cada VM y sobreviven al apagado del laboratorio.
