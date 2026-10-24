# Diagramas técnicos de DFSha

Fuente editable en Mermaid. Los diagramas describen la entrega final: REST/HTTPS para las APIs y transporte Raft con TLS mutuo para consenso entre ControlNodes. La topología de tres VMs es el despliegue previsto; ejecutar Compose en una computadora solo comprueba procesos y contenedores separados.

## 1. Arquitectura general

```mermaid
flowchart LR
    C[Cliente CLI Python]
    subgraph Control[Plano de control Go]
        C1[cn-01]
        C2[cn-02]
        C3[cn-03]
        C1 <-->|Raft y TLS mutuo| C2
        C2 <-->|Raft y TLS mutuo| C3
        C1 <-->|Raft y TLS mutuo| C3
    end
    subgraph Datos[Plano de datos Go]
        D1[dn-01: bloques AES-GCM]
        D2[dn-02: bloques AES-GCM]
        D3[dn-03: bloques AES-GCM]
        D1 <-->|HTTPS binario| D2
        D2 <-->|HTTPS binario| D3
        D1 <-->|HTTPS binario| D3
    end
    C -->|HTTPS JSON: sesión y planes| Control
    C <-->|HTTPS binario y capabilities| Datos
    Datos -->|Registro, heartbeat e inventario| Control
    Control -->|Verificación, copia y limpieza| Datos
```

## 2. Distribución física

```mermaid
flowchart TB
    U[Cliente con acceso a la red privada o VPN]
    subgraph VPC[Red privada de AWS Academy]
        subgraph VM1[VM 1: Ubuntu y Docker]
            CN1[cn-01: API 8000 y Raft 9000]
            DN1[dn-01: API 8001]
            M1[(Metadatos persistentes)]
            B1[(Volumen de bloques cifrados)]
            CN1 --- M1
            DN1 --- B1
        end
        subgraph VM2[VM 2: Ubuntu y Docker]
            CN2[cn-02: API 8000 y Raft 9000]
            DN2[dn-02: API 8001]
            M2[(Metadatos persistentes)]
            B2[(Volumen de bloques cifrados)]
            CN2 --- M2
            DN2 --- B2
        end
        subgraph VM3[VM 3: Ubuntu y Docker]
            CN3[cn-03: API 8000 y Raft 9000]
            DN3[dn-03: API 8001]
            M3[(Metadatos persistentes)]
            B3[(Volumen de bloques cifrados)]
            CN3 --- M3
            DN3 --- B3
        end
        VM4[VM 4 opcional: dn-04]
        CN1 <--> CN2
        CN2 <--> CN3
        DN1 <--> DN2
        DN2 <--> DN3
    end
    U -->|HTTPS a CN y DN| VPC
```

## 3. Secuencia de subida

```mermaid
sequenceDiagram
    participant C as Cliente
    participant L as CN líder
    participant Q as Mayoría Raft
    participant A as DN A
    participant B as DN B
    C->>L: login y upload/plan(path,size)
    L->>Q: Persistir reserva y plan
    Q-->>L: Commit
    L-->>C: upload_id, bloques, destinos y capability
    loop Hasta cuatro bloques concurrentes
        C->>C: Leer bloque y calcular SHA-256
        C->>A: PUT binario con autorización del plan
        A->>A: Verificar SHA y guardar AES-GCM
        A->>B: Propagar por HTTPS
        B->>B: Verificar y guardar AES-GCM
        B-->>A: Copia persistida
        A-->>C: stored_on
    end
    C->>L: upload/commit(upload_id,blocks,sha256)
    L->>A: HEAD de bloques planificados
    L->>B: HEAD de bloques planificados
    A-->>L: Tamaño y SHA verificados
    B-->>L: Tamaño y SHA verificados
    L->>Q: Publicar archivo y liberar reserva
    Q-->>L: Commit
    L-->>C: Archivo publicado
```

## 4. Secuencia de descarga

```mermaid
sequenceDiagram
    participant C as Cliente
    participant L as CN líder
    participant Q as Mayoría Raft
    participant A as DN A
    participant B as DN B
    C->>L: download/plan(path)
    L->>Q: Verificar liderazgo y aplicar pendientes
    Q-->>L: Lectura autoritativa habilitada
    L-->>C: Bloques y réplicas con capabilities
    par Bloques concurrentes
        C->>A: GET bloque
        alt Réplica válida
            A-->>C: Binario
        else Caída o integridad inválida
            C->>B: GET de la siguiente réplica
            B-->>C: Binario válido
        end
        C->>C: SHA-256 del bloque y escritura por offset
    end
    C->>C: Validar SHA-256 del archivo y renombrar .part
```

## 5. Registro, heartbeat e inventario

```mermaid
sequenceDiagram
    participant D as DataNode
    participant C as CN disponible
    participant L as CN líder
    participant Q as Mayoría Raft
    D->>C: POST /cluster/register
    alt C es seguidor
        C-->>D: 503 con líder conocido
        D->>L: Reintentar registro
    else C es líder
        C->>L: Procesar localmente
    end
    L->>Q: Persistir dirección y capacidad
    Q-->>L: Commit
    L-->>D: Registro aceptado
    loop Cada 3 segundos
        D->>L: Heartbeat con uso y capacidad
        L-->>D: Aceptado
    end
    loop Cada 30 segundos
        D->>D: Verificar inventario local
        D->>L: blockreport con bloques válidos
        L->>Q: Conciliar ubicaciones
        Q-->>L: Commit
        L-->>D: Reporte aceptado
    end
    Note over L: Sin heartbeat durante 9 s: DN excluido de planes nuevos
```

## 6. Replicación y reparación

```mermaid
sequenceDiagram
    participant L as CN líder
    participant Q as Mayoría Raft
    participant A as DN con copia válida
    participant B as DN destino
    participant O as DN origen de balanceo
    L->>L: Detectar falta de réplica o diferencia de ocupación mayor al 10 %
    L->>A: Orden replicate con destino B
    A->>A: Descifrar y verificar bloque
    A->>B: PUT binario autenticado
    B->>B: Verificar SHA y persistir AES-GCM
    B-->>A: Copia completa
    A-->>L: Réplica confirmada
    L->>B: HEAD de comprobación
    B-->>L: Tamaño y SHA válidos
    L->>Q: Añadir ubicación B
    Q-->>L: Commit
    opt Hay copia sobrante y es seguro retirar
        L->>Q: Retirar ubicación O conservando réplicas válidas
        Q-->>L: Commit
        Note over L,O: Esperar vencimiento de autorizaciones de lectura
        L->>O: DELETE interno
        O-->>L: Eliminada
    end
```

## 7. Elección de líder

```mermaid
sequenceDiagram
    participant X as Líder anterior
    participant A as cn-02
    participant B as cn-03
    Note over X: Se detiene o pierde conectividad
    A->>A: Vence timeout de elección
    A->>A: Incrementar término y persistir voto
    A->>B: RequestVote con término e información del registro
    B->>B: Validar registro y voto del término
    B-->>A: Voto concedido
    Note over A,B: Dos de tres forman mayoría
    A->>B: AppendEntries y heartbeat Raft
    B-->>A: Aceptado
    A->>A: Aplicar entradas confirmadas
    Note over A: Habilita solicitudes al demostrar liderazgo
    opt Líder anterior vuelve
        X->>A: Comunicación con término antiguo
        A-->>X: Término vigente
        X->>X: Pasa a seguidor
    end
```

## 8. Recuperación ante fallos

```mermaid
flowchart TD
    F[Proceso o VM se detiene] --> K{Componente}
    K -->|ControlNode| CQ{Hay dos de tres CN conectados}
    CQ -->|Sí| E[Raft elige o conserva líder]
    E --> R[Recuperar registro y snapshots persistentes]
    R --> S[Continuar metadatos confirmados]
    CQ -->|No| U[Rechazar operaciones autoritativas hasta recuperar mayoría]
    K -->|DataNode| D[Excluir tras timeout de heartbeat]
    D --> V{Queda una réplica válida}
    V -->|Sí| G[Leer otra copia y reparar hacia DN disponible]
    V -->|No| N[Error de disponibilidad: no inventar contenido]
    G --> I[Inventario al reincorporarse el nodo]
    I --> C[Conciliar copias y retirar sobrantes de forma segura]
    F --> P[Subida sin commit]
    P --> T[Reserva vence; archivo no publicado]
    T --> GC[Recolectar huérfanos tras periodo de seguridad]
```

El algoritmo Raft considera fallos de parada y pérdida de comunicaciones, con almacenamiento persistente que respeta las escrituras. No se afirma tolerancia bizantina ni recuperación después de perder todas las copias válidas de datos o una mayoría de discos de control.
