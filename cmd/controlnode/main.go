// ControlNode de DFSha: guarda los metadatos del sistema de archivos, sabe
// qué DataNodes están vivos y calcula los planes de lectura y escritura.
// Nunca recibe los datos de los archivos.
package main

import (
	"log"
	"net/http"
)

func main() {
	cfg, err := loadConfig()
	if err != nil {
		log.Fatalf("configuración inválida: %v", err)
	}
	s := newServer(cfg)
	log.Printf("ControlNode %s escuchando en %s (replication_factor=%d, bloque=%d-%d MB, k=%d, nodo muerto tras %s)",
		cfg.NodeID, cfg.Addr, cfg.ReplicationFactor,
		cfg.BlockSizing.Min/megabyte, cfg.BlockSizing.Max/megabyte, cfg.BlockSizing.BlocksPerNode,
		cfg.HeartbeatTimeout())
	log.Fatal(http.ListenAndServe(cfg.Addr, s.routes()))
}
