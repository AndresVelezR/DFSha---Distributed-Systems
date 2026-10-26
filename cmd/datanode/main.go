// DataNode DFSha: almacenamiento de bloques autenticados e inmutables.
package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"dfsha/internal/common"
)

const maxBlockSize int64 = 64 * 1024 * 1024

type Node struct {
	ID          string
	URL         string
	Addr        string
	ControlURLs []string
	StorageDir  string
	Capacity    int64
	Config      common.Config
	Client      *http.Client

	storageMu sync.Mutex              // protege escrituras, borrados e inventario
	cacheMu   sync.Mutex              // protege verified; nunca se toma durante un descifrado
	verified  map[string]verifiedFile // bloques ya verificados, ver inventoryLocked
	ownersMu  sync.Mutex              // protege el registro de autoría, ver ownership.go
}

type blockInfo struct {
	BlockID string `json:"block_id"`
	Size    int64  `json:"size"`
}

type forwardTarget struct {
	Node  string `json:"node"`
	URL   string `json:"url"`
	Token string `json:"token,omitempty"`
}

func main() {
	n, err := loadNode()
	if err != nil {
		log.Fatal(err)
	}
	if err := os.MkdirAll(n.StorageDir, 0700); err != nil {
		log.Fatal(err)
	}
	entries, err := os.ReadDir(n.StorageDir)
	if err != nil {
		log.Fatal(err)
	}
	// Temporales de procesos anteriores nunca se anuncian como bloques.
	for _, e := range entries {
		if !e.IsDir() && strings.HasPrefix(e.Name(), ".block-") {
			if err := os.Remove(filepath.Join(n.StorageDir, e.Name())); err != nil {
				log.Printf("limpiar temporal: %v", err)
			}
		}
	}
	go n.controlLoop(context.Background())
	log.Printf("DataNode %s en %s, almacenamiento=%s, capacidad=%d", n.ID, n.Addr, n.StorageDir, n.Capacity)
	log.Fatal(common.Serve(n.Addr, n.routes(), n.Config))
}

func (n *Node) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", n.health)
	mux.HandleFunc("/inventory", n.inventory)
	mux.HandleFunc("/blocks/", n.blocks)
	return mux
}
