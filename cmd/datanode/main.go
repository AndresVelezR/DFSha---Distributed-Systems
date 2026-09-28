package main

import (
	"bytes"
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
	"syscall"
	"time"
)

type Node struct {
	ID          string
	Host        string
	Port        int
	ControlURL  string
	StorageDir  string
	ClusterKey  string
	ClientToken string
}

func main() {
	port, _ := strconv.Atoi(getenv("PORT", "8001"))
	n := &Node{ID: getenv("NODE_ID", "dn-01"), Host: getenv("ADVERTISE_HOST", "localhost"), Port: port, ControlURL: getenv("CONTROL_URL", "http://localhost:8000"), StorageDir: getenv("STORAGE_DIR", "./data"), ClusterKey: getenv("CLUSTER_KEY", "dev-cluster-key"), ClientToken: getenv("CLIENT_TOKEN", "dfsha-dev-token")}
	if err := os.MkdirAll(n.StorageDir, 0755); err != nil {
		log.Fatal(err)
	}
	if err := n.register(); err != nil {
		log.Printf("registro inicial falló: %v (se reintentará)", err)
	}
	go n.heartbeatLoop()
	mux := http.NewServeMux()
	mux.HandleFunc("/health", n.health)
	mux.HandleFunc("/blocks/", n.blocks)
	addr := fmt.Sprintf(":%d", n.Port)
	log.Printf("DataNode %s escuchando en %s, almacenamiento=%s", n.ID, addr, n.StorageDir)
	log.Fatal(http.ListenAndServe(addr, mux))
}

func getenv(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

func (n *Node) register() error {
	cap, used := diskUsage(n.StorageDir)
	body := map[string]any{"node_id": n.ID, "host": n.Host, "port": n.Port, "capacity": cap, "used": used}
	return n.postControl("/cluster/register", body)
}

func (n *Node) heartbeatLoop() {
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		cap, used := diskUsage(n.StorageDir)
		body := map[string]any{"node_id": n.ID, "capacity": cap, "used": used}
		if err := n.postControl("/cluster/heartbeat", body); err != nil {
			log.Printf("heartbeat: %v; reintentando registro", err)
			_ = n.register()
		}
	}
}

func (n *Node) postControl(endpoint string, body any) error {
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, n.ControlURL+endpoint, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Cluster-Key", n.ClusterKey)
	c := &http.Client{Timeout: 4 * time.Second}
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		x, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("%s: %s", resp.Status, string(x))
	}
	return nil
}

func (n *Node) health(w http.ResponseWriter, r *http.Request) {
	cap, used := diskUsage(n.StorageDir)
	writeJSON(w, http.StatusOK, map[string]any{"node_id": n.ID, "capacity": cap, "used": used, "n_blocks": countBlocks(n.StorageDir)})
}

func (n *Node) authorized(r *http.Request) bool {
	return r.Header.Get("Authorization") == "Bearer "+n.ClientToken || r.Header.Get("X-Cluster-Key") == n.ClusterKey
}

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

func (n *Node) forwardFile(base, id, file string) error {
	f, err := os.Open(file)
	if err != nil {
		return err
	}
	defer f.Close()
	req, err := http.NewRequest(http.MethodPut, strings.TrimRight(base, "/")+"/blocks/"+id, f)
	if err != nil {
		return err
	}
	req.Header.Set("X-Cluster-Key", n.ClusterKey)
	req.Header.Set("Content-Type", "application/octet-stream")
	c := &http.Client{Timeout: 2 * time.Minute}
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("%s %s", resp.Status, string(b))
	}
	return nil
}

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

func diskUsage(p string) (int64, int64) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(p, &st); err != nil {
		return 0, 0
	}
	cap := int64(st.Blocks) * int64(st.Bsize)
	free := int64(st.Bavail) * int64(st.Bsize)
	return cap, cap - free
}
func countBlocks(dir string) int {
	es, _ := os.ReadDir(dir)
	n := 0
	for _, e := range es {
		if !e.IsDir() && !strings.HasSuffix(e.Name(), ".tmp") {
			n++
		}
	}
	return n
}
func fileSize(p string) int64 {
	st, err := os.Stat(p)
	if err != nil {
		return 0
	}
	return st.Size()
}
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
