package main

import "time"

// NodeInfo es lo que el ControlNode sabe de un DataNode registrado.
type NodeInfo struct {
	NodeID   string    `json:"node_id"`
	Host     string    `json:"host"`
	Port     int       `json:"port"`
	Capacity int64     `json:"capacity"`
	Used     int64     `json:"used"`
	LastSeen time.Time `json:"last_seen"`
}

// Target es la dirección de un DataNode tal como la recibe el cliente en un plan.
type Target struct {
	Node string `json:"node"`
	Host string `json:"host"`
	Port int    `json:"port"`
}

// BlockPlan indica a qué DataNodes debe ir un bloque. El primer destino recibe
// el bloque del cliente y lo propaga a los demás.
type BlockPlan struct {
	Index   int      `json:"index"`
	Targets []Target `json:"targets"`
}

// UploadSession es una subida en curso: reserva la ruta hasta ExpiresAt y
// guarda el plan entregado al cliente. El archivo no existe hasta el commit.
type UploadSession struct {
	UploadID  string      `json:"upload_id"`
	Owner     string      `json:"owner"`
	Path      string      `json:"path"`
	Size      int64       `json:"size"`
	BlockSize int64       `json:"block_size"`
	NBlocks   int         `json:"n_blocks"`
	ExpiresAt time.Time   `json:"expires_at"`
	Blocks    []BlockPlan `json:"blocks"`
}

// StoredBlock es un bloque ya escrito: su posición en el archivo, su firma
// SHA-256 (que también es su identificador) y los DataNodes que lo tienen.
type StoredBlock struct {
	Index    int      `json:"index"`
	BlockID  string   `json:"block_id"`
	Size     int64    `json:"size"`
	StoredOn []string `json:"stored_on"`
}

// FileMeta son los metadatos de un archivo publicado.
type FileMeta struct {
	Path      string        `json:"path"`
	Owner     string        `json:"owner"`
	Size      int64         `json:"size"`
	BlockSize int64         `json:"block_size"`
	Blocks    []StoredBlock `json:"blocks"`
	CreatedAt time.Time     `json:"created_at"`
}
