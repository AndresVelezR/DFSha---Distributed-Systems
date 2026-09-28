// DataNode de DFSha: guarda bloques en su disco local, los entrega cuando se
// los piden y propaga copias a otros DataNodes.
package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"time"
)

// Node es la configuración de este DataNode y de a quién le habla.
type Node struct {
	ID                string
	Host              string // dirección con la que otros nodos y el cliente lo alcanzan
	Port              int
	ControlURL        string
	StorageDir        string
	ClusterKey        string
	ClientToken       string
	HeartbeatInterval time.Duration
}

func main() {
	n, err := loadNode()
	if err != nil {
		log.Fatalf("configuración inválida: %v", err)
	}
	if err := os.MkdirAll(n.StorageDir, 0755); err != nil {
		log.Fatal(err)
	}
	if err := n.register(); err != nil {
		log.Printf("registro inicial falló: %v (se reintentará)", err)
	}
	go n.heartbeatLoop()

	addr := fmt.Sprintf(":%d", n.Port)
	log.Printf("DataNode %s escuchando en %s, almacenamiento=%s, heartbeat cada %s", n.ID, addr, n.StorageDir, n.HeartbeatInterval)
	log.Fatal(http.ListenAndServe(addr, n.routes()))
}

func (n *Node) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", n.health)
	mux.HandleFunc("/blocks/", n.blocks)
	return mux
}
