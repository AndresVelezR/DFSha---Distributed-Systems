package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func (n *Node) health(w http.ResponseWriter, r *http.Request) {
	capacity, used := diskUsage(n.StorageDir)
	writeJSON(w, http.StatusOK, map[string]any{"node_id": n.ID, "capacity": capacity, "used": used, "n_blocks": countBlocks(n.StorageDir)})
}

// authorized acepta al cliente (token) o a otro nodo del clúster (clave).
func (n *Node) authorized(r *http.Request) bool {
	return r.Header.Get("Authorization") == "Bearer "+n.ClientToken || r.Header.Get("X-Cluster-Key") == n.ClusterKey
}

// blocks enruta /blocks/{id} y /blocks/{id}/replicate.
func (n *Node) blocks(w http.ResponseWriter, r *http.Request) {
	if !n.authorized(r) {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "no autorizado"})
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/blocks/")
	if rest == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "block id requerido"})
		return
	}
	if strings.HasSuffix(rest, "/replicate") {
		n.replicate(w, r, strings.TrimSuffix(rest, "/replicate"))
		return
	}
	id := filepath.Base(rest)
	if len(id) != 64 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "block id debe ser SHA-256 hexadecimal"})
		return
	}
	switch r.Method {
	case http.MethodPut:
		n.putBlock(w, r, id)
	case http.MethodGet:
		n.getBlock(w, r, id)
	case http.MethodDelete:
		n.deleteBlock(w, r, id)
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "método no permitido"})
	}
}

// putBlock guarda el bloque solo si su SHA-256 coincide con el id, y después
// lo propaga a los DataNodes indicados en X-Forward-To. Se escribe primero a
// un .tmp y se renombra, para que nunca quede visible un bloque a medias.
func (n *Node) putBlock(w http.ResponseWriter, r *http.Request, id string) {
	dst := filepath.Join(n.StorageDir, id)
	if _, err := os.Stat(dst); err == nil {
		writeJSON(w, http.StatusOK, map[string]any{"block_id": id, "size": fileSize(dst), "stored_on": []string{n.ID}})
		return
	}
	tmp := dst + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	h := sha256.New()
	size, err := io.Copy(io.MultiWriter(f, h), r.Body)
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		_ = os.Remove(tmp)
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "falló escritura"})
		return
	}
	got := hex.EncodeToString(h.Sum(nil))
	if got != id {
		_ = os.Remove(tmp)
		writeJSON(w, http.StatusConflict, map[string]any{"error": "SHA-256 no coincide", "expected": id, "got": got})
		return
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	stored := []string{n.ID}
	forward := strings.TrimSpace(r.Header.Get("X-Forward-To"))
	if forward != "" {
		for _, target := range strings.Split(forward, ",") {
			target = strings.TrimSpace(target)
			if target == "" {
				continue
			}
			nodeID := r.Header.Get("X-Forward-Node-" + strconv.Itoa(len(stored)))
			if err := n.forwardFile(target, id, dst); err != nil {
				log.Printf("forward %s -> %s falló: %v", id, target, err)
				continue
			}
			if nodeID != "" {
				stored = append(stored, nodeID)
			} else {
				stored = append(stored, target)
			}
		}
	}
	writeJSON(w, http.StatusCreated, map[string]any{"block_id": id, "size": size, "stored_on": stored})
}

// getBlock entrega el bloque; http.ServeContent atiende la cabecera Range.
func (n *Node) getBlock(w http.ResponseWriter, r *http.Request, id string) {
	p := filepath.Join(n.StorageDir, id)
	f, err := os.Open(p)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "bloque no existe"})
		return
	}
	defer f.Close()
	st, _ := f.Stat()
	w.Header().Set("Content-Type", "application/octet-stream")
	http.ServeContent(w, r, id, st.ModTime(), f)
}

// deleteBlock solo lo puede pedir un nodo del clúster, nunca el cliente.
func (n *Node) deleteBlock(w http.ResponseWriter, r *http.Request, id string) {
	if r.Header.Get("X-Cluster-Key") != n.ClusterKey {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "requiere X-Cluster-Key"})
		return
	}
	if err := os.Remove(filepath.Join(n.StorageDir, id)); err != nil && os.IsNotExist(err) {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "bloque no existe"})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// replicate copia un bloque local a otro DataNode por orden del clúster.
func (n *Node) replicate(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "método no permitido"})
		return
	}
	if r.Header.Get("X-Cluster-Key") != n.ClusterKey {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "requiere X-Cluster-Key"})
		return
	}
	var req struct {
		Target struct {
			Host string `json:"host"`
			Port int    `json:"port"`
		} `json:"target"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	base := fmt.Sprintf("http://%s:%d", req.Target.Host, req.Target.Port)
	if err := n.forwardFile(base, id, filepath.Join(n.StorageDir, id)); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"status": "replication_started", "target": base})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
