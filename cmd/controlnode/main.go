// ControlNode de DFSha: guarda los metadatos del sistema de archivos, sabe
// qué DataNodes están vivos y calcula los planes de lectura y escritura.
// Nunca recibe los datos de los archivos.
package main

import (
	"log"
	"net/http"
	"os"
	"time"
)

const (
	demoUser          = "demo"
	demoPassword      = "demo"
	demoToken         = "dfsha-dev-token"
	replicationFactor = 2
	blockMin          = int64(8 * 1024 * 1024)
	blockMax          = int64(64 * 1024 * 1024)
	blocksPerNode     = int64(4)
	heartbeatTimeout  = 10 * time.Second
)

func main() {
	addr := getenv("CN_ADDR", ":8000")
	s := newServer()
	log.Printf("ControlNode escuchando en %s", addr)
	log.Fatal(http.ListenAndServe(addr, s.routes()))
}

func getenv(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
