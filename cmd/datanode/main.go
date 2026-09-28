// DataNode de DFSha: guarda bloques en su disco local, los entrega cuando se
// los piden y propaga copias a otros DataNodes.
package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
)

// Node es la configuración de este DataNode y de a quién le habla.
type Node struct {
	ID          string
	Host        string // dirección con la que otros nodos y el cliente lo alcanzan
	Port        int
	ControlURL  string
	StorageDir  string
	ClusterKey  string
	ClientToken string
}

func main() {
	port, _ := strconv.Atoi(getenv("PORT", "8001"))
	n := &Node{
		ID:          getenv("NODE_ID", "dn-01"),
		Host:        getenv("ADVERTISE_HOST", "localhost"),
		Port:        port,
		ControlURL:  getenv("CONTROL_URL", "http://localhost:8000"),
		StorageDir:  getenv("STORAGE_DIR", "./data"),
		ClusterKey:  getenv("CLUSTER_KEY", "dev-cluster-key"),
		ClientToken: getenv("CLIENT_TOKEN", "dfsha-dev-token"),
	}
	if err := os.MkdirAll(n.StorageDir, 0755); err != nil {
		log.Fatal(err)
	}
	if err := n.register(); err != nil {
		log.Printf("registro inicial falló: %v (se reintentará)", err)
	}
	go n.heartbeatLoop()

	addr := fmt.Sprintf(":%d", n.Port)
	log.Printf("DataNode %s escuchando en %s, almacenamiento=%s", n.ID, addr, n.StorageDir)
	log.Fatal(http.ListenAndServe(addr, n.routes()))
}

func (n *Node) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", n.health)
	mux.HandleFunc("/blocks/", n.blocks)
	return mux
}

func getenv(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
