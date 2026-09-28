package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/http"
	"os"
	"path"
	"sort"
	"strings"
	"sync"
	"time"
)

type NodeInfo struct {
	NodeID   string    `json:"node_id"`
	Host     string    `json:"host"`
	Port     int       `json:"port"`
	Capacity int64     `json:"capacity"`
	Used     int64     `json:"used"`
	LastSeen time.Time `json:"last_seen"`
}

type Target struct {
	Node string `json:"node"`
	Host string `json:"host"`
	Port int    `json:"port"`
}

type BlockPlan struct {
	Index   int      `json:"index"`
	Targets []Target `json:"targets"`
}

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

type StoredBlock struct {
	Index    int      `json:"index"`
	BlockID  string   `json:"block_id"`
	Size     int64    `json:"size"`
	StoredOn []string `json:"stored_on"`
}

type FileMeta struct {
	Path      string        `json:"path"`
	Owner     string        `json:"owner"`
	Size      int64         `json:"size"`
	BlockSize int64         `json:"block_size"`
	Blocks    []StoredBlock `json:"blocks"`
	CreatedAt time.Time     `json:"created_at"`
}

type Server struct {
	mu      sync.RWMutex
	nodes   map[string]*NodeInfo
	uploads map[string]*UploadSession
	files   map[string]*FileMeta
	dirs    map[string]bool
}

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
	s := &Server{
		nodes:   map[string]*NodeInfo{},
		uploads: map[string]*UploadSession{},
		files:   map[string]*FileMeta{},
		dirs:    map[string]bool{"/": true},
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/health", s.health)
	mux.HandleFunc("/auth/login", s.login)
	mux.HandleFunc("/fs/ls", s.withAuth(s.fsLS))
	mux.HandleFunc("/fs/mkdir", s.withAuth(s.fsMkdir))
	mux.HandleFunc("/fs/rmdir", s.withAuth(s.fsRmdir))
	mux.HandleFunc("/fs/rm", s.withAuth(s.fsRM))
	mux.HandleFunc("/fs/mv", s.withAuth(s.fsMV))
	mux.HandleFunc("/fs/stat", s.withAuth(s.fsStat))
	mux.HandleFunc("/files/upload/plan", s.withAuth(s.uploadPlan))
	mux.HandleFunc("/files/upload/commit", s.withAuth(s.uploadCommit))
	mux.HandleFunc("/files/upload/abort", s.withAuth(s.uploadAbort))
	mux.HandleFunc("/files/download/plan", s.withAuth(s.downloadPlan))
	mux.HandleFunc("/cluster/register", s.withClusterKey(s.clusterRegister))
	mux.HandleFunc("/cluster/heartbeat", s.withClusterKey(s.clusterHeartbeat))
	mux.HandleFunc("/cluster/blockreport", s.withClusterKey(s.clusterBlockReport))
	mux.HandleFunc("/cluster/replicate", s.notImplemented)
	mux.HandleFunc("/cluster/election", s.notImplemented)

	log.Printf("ControlNode escuchando en %s", addr)
	log.Fatal(http.ListenAndServe(addr, logging(mux)))
}

func getenv(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

func logging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		log.Printf("%s %s %s", r.Method, r.URL.RequestURI(), time.Since(start))
	})
}

func (s *Server) withAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+demoToken {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "token inválido"})
			return
		}
		next(w, r)
	}
}

func (s *Server) withClusterKey(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Cluster-Key") != getenv("CLUSTER_KEY", "dev-cluster-key") {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "cluster key inválida"})
			return
		}
		next(w, r)
	}
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	alive := 0
	for _, n := range s.nodes {
		if time.Since(n.LastSeen) <= heartbeatTimeout {
			alive++
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"node_id": getenv("NODE_ID", "cn-01"), "role": "leader-hito2", "alive_datanodes": alive, "files": len(s.files)})
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var req struct{ Username, Password string }
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		badRequest(w, err)
		return
	}
	if req.Username != demoUser || req.Password != demoPassword {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "credenciales inválidas"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"token": demoToken, "expires_in": 3600})
}

func cleanRemote(p string) string {
	if p == "" {
		return "/"
	}
	p = path.Clean("/" + strings.TrimPrefix(p, "/"))
	return p
}

func (s *Server) fsLS(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	base := cleanRemote(r.URL.Query().Get("path"))
	s.mu.RLock()
	defer s.mu.RUnlock()
	if !s.dirs[base] {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "directorio no existe"})
		return
	}
	prefix := base
	if prefix != "/" {
		prefix += "/"
	}
	entries := []map[string]any{}
	seen := map[string]bool{}
	for d := range s.dirs {
		if d == base || !strings.HasPrefix(d, prefix) {
			continue
		}
		rest := strings.TrimPrefix(d, prefix)
		if rest == "" || strings.Contains(rest, "/") {
			continue
		}
		if !seen[rest] {
			entries = append(entries, map[string]any{"name": rest, "type": "dir"})
			seen[rest] = true
		}
	}
	for p, f := range s.files {
		if !strings.HasPrefix(p, prefix) {
			continue
		}
		rest := strings.TrimPrefix(p, prefix)
		if rest == "" || strings.Contains(rest, "/") {
			continue
		}
		entries = append(entries, map[string]any{"name": rest, "type": "file", "size": f.Size, "blocks": len(f.Blocks), "replicas_ok": minReplicas(f.Blocks)})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i]["name"].(string) < entries[j]["name"].(string) })
	writeJSON(w, http.StatusOK, map[string]any{"path": base, "entries": entries})
}

func minReplicas(blocks []StoredBlock) int {
	if len(blocks) == 0 {
		return 0
	}
	m := 1 << 30
	for _, b := range blocks {
		if len(b.StoredOn) < m {
			m = len(b.StoredOn)
		}
	}
	return m
}

func (s *Server) fsMkdir(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var req struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		badRequest(w, err)
		return
	}
	p := cleanRemote(req.Path)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.dirs[p] {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "ya existe"})
		return
	}
	parent := path.Dir(p)
	if !s.dirs[parent] {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "directorio padre no existe"})
		return
	}
	s.dirs[p] = true
	writeJSON(w, http.StatusCreated, map[string]any{"path": p})
}

func (s *Server) fsRmdir(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		methodNotAllowed(w)
		return
	}
	p := cleanRemote(r.URL.Query().Get("path"))
	if p == "/" {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "no se puede borrar /"})
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.dirs[p] {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "no existe"})
		return
	}
	prefix := p + "/"
	for d := range s.dirs {
		if d != p && strings.HasPrefix(d, prefix) {
			writeJSON(w, http.StatusConflict, map[string]any{"error": "directorio no vacío"})
			return
		}
	}
	for f := range s.files {
		if strings.HasPrefix(f, prefix) {
			writeJSON(w, http.StatusConflict, map[string]any{"error": "directorio no vacío"})
			return
		}
	}
	delete(s.dirs, p)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) fsRM(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		methodNotAllowed(w)
		return
	}
	p := cleanRemote(r.URL.Query().Get("path"))
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.files[p]; !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "archivo no existe"})
		return
	}
	delete(s.files, p)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) fsMV(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var req struct {
		Src string `json:"src"`
		Dst string `json:"dst"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		badRequest(w, err)
		return
	}
	src, dst := cleanRemote(req.Src), cleanRemote(req.Dst)
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.files[dst]; exists {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "destino existe"})
		return
	}
	f, ok := s.files[src]
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "origen no existe"})
		return
	}
	if !s.dirs[path.Dir(dst)] {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "directorio destino no existe"})
		return
	}
	delete(s.files, src)
	f.Path = dst
	s.files[dst] = f
	writeJSON(w, http.StatusOK, map[string]any{"src": src, "dst": dst})
}

func (s *Server) fsStat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	p := cleanRemote(r.URL.Query().Get("path"))
	s.mu.RLock()
	defer s.mu.RUnlock()
	f, ok := s.files[p]
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "archivo no existe"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"size": f.Size, "block_size": f.BlockSize, "n_blocks": len(f.Blocks), "replicas_ok": minReplicas(f.Blocks), "owner": f.Owner})
}

func (s *Server) aliveNodes() []*NodeInfo {
	nodes := []*NodeInfo{}
	for _, n := range s.nodes {
		if time.Since(n.LastSeen) <= heartbeatTimeout {
			copyN := *n
			nodes = append(nodes, &copyN)
		}
	}
	return nodes
}

func chooseBlockSize(size int64, n int) int64 {
	if size <= 0 || n <= 0 {
		return blockMin
	}
	proposed := size / (blocksPerNode * int64(n))
	if proposed < blockMin {
		return blockMin
	}
	if proposed > blockMax {
		return blockMax
	}
	// Potencia de dos inferior, en bytes.
	p := int64(1) << (63 - bitsLeadingZeros64(uint64(proposed)))
	if p < blockMin {
		p = blockMin
	}
	if p > blockMax {
		p = blockMax
	}
	return p
}

func bitsLeadingZeros64(x uint64) int {
	if x == 0 {
		return 64
	}
	n := 0
	mask := uint64(1) << 63
	for mask > 0 && x&mask == 0 {
		n++
		mask >>= 1
	}
	return n
}

func (s *Server) uploadPlan(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var req struct {
		Path string `json:"path"`
		Size int64  `json:"size"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		badRequest(w, err)
		return
	}
	remote := cleanRemote(req.Path)
	if req.Size < 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "size inválido"})
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.dirs[path.Dir(remote)] {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "directorio padre no existe"})
		return
	}
	nodes := s.aliveNodes()
	if len(nodes) == 0 {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "no hay DataNodes vivos"})
		return
	}
	blockSize := chooseBlockSize(req.Size, len(nodes))
	nBlocks := int(math.Ceil(float64(req.Size) / float64(blockSize)))
	if req.Size == 0 {
		nBlocks = 1
	}
	rf := replicationFactor
	if rf > len(nodes) {
		rf = len(nodes)
	}
	plans := make([]BlockPlan, nBlocks)
	// Carga virtual permite repartir uniformemente los bloques del mismo plan.
	virtualUsed := map[string]int64{}
	for _, n := range nodes {
		virtualUsed[n.NodeID] = n.Used
	}
	for i := 0; i < nBlocks; i++ {
		candidates := append([]*NodeInfo(nil), nodes...)
		sort.Slice(candidates, func(a, b int) bool {
			ra := float64(virtualUsed[candidates[a].NodeID]) / float64(max64(candidates[a].Capacity, 1))
			rb := float64(virtualUsed[candidates[b].NodeID]) / float64(max64(candidates[b].Capacity, 1))
			return ra < rb
		})
		targets := []Target{}
		for j := 0; j < rf; j++ {
			n := candidates[j]
			targets = append(targets, Target{Node: n.NodeID, Host: n.Host, Port: n.Port})
			virtualUsed[n.NodeID] += blockSize
		}
		plans[i] = BlockPlan{Index: i, Targets: targets}
	}
	id := randomID()
	up := &UploadSession{UploadID: id, Owner: demoUser, Path: remote, Size: req.Size, BlockSize: blockSize, NBlocks: nBlocks, ExpiresAt: time.Now().Add(60 * time.Second), Blocks: plans}
	s.uploads[id] = up
	writeJSON(w, http.StatusOK, map[string]any{"upload_id": id, "block_size": blockSize, "n_blocks": nBlocks, "lease_expires_at": up.ExpiresAt, "blocks": plans})
}

func (s *Server) uploadCommit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var req struct {
		UploadID string        `json:"upload_id"`
		Blocks   []StoredBlock `json:"blocks"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		badRequest(w, err)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	up, ok := s.uploads[req.UploadID]
	if !ok {
		writeJSON(w, http.StatusGone, map[string]any{"error": "upload_id inexistente o vencido"})
		return
	}
	if time.Now().After(up.ExpiresAt) {
		delete(s.uploads, req.UploadID)
		writeJSON(w, http.StatusGone, map[string]any{"error": "reserva vencida"})
		return
	}
	if len(req.Blocks) != up.NBlocks {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "faltan bloques"})
		return
	}
	sort.Slice(req.Blocks, func(i, j int) bool { return req.Blocks[i].Index < req.Blocks[j].Index })
	for i, b := range req.Blocks {
		if b.Index != i || b.BlockID == "" || len(b.StoredOn) == 0 {
			writeJSON(w, http.StatusConflict, map[string]any{"error": fmt.Sprintf("bloque %d inválido", i)})
			return
		}
	}
	s.files[up.Path] = &FileMeta{Path: up.Path, Owner: up.Owner, Size: up.Size, BlockSize: up.BlockSize, Blocks: req.Blocks, CreatedAt: time.Now()}
	delete(s.uploads, req.UploadID)
	writeJSON(w, http.StatusOK, map[string]any{"status": "publicado", "path": up.Path})
}

func (s *Server) uploadAbort(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var req struct {
		UploadID string `json:"upload_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		badRequest(w, err)
		return
	}
	s.mu.Lock()
	delete(s.uploads, req.UploadID)
	s.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) downloadPlan(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	p := cleanRemote(r.URL.Query().Get("path"))
	s.mu.RLock()
	defer s.mu.RUnlock()
	f, ok := s.files[p]
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "archivo no existe"})
		return
	}
	blocks := []map[string]any{}
	for _, b := range f.Blocks {
		replicas := []Target{}
		for _, id := range b.StoredOn {
			if n, ok := s.nodes[id]; ok && time.Since(n.LastSeen) <= heartbeatTimeout {
				replicas = append(replicas, Target{Node: n.NodeID, Host: n.Host, Port: n.Port})
			}
		}
		if len(replicas) == 0 {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "bloque sin copias vivas", "block_id": b.BlockID})
			return
		}
		blocks = append(blocks, map[string]any{"index": b.Index, "block_id": b.BlockID, "size": b.Size, "replicas": replicas})
	}
	writeJSON(w, http.StatusOK, map[string]any{"path": p, "size": f.Size, "block_size": f.BlockSize, "blocks": blocks})
}

func (s *Server) clusterRegister(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var req NodeInfo
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		badRequest(w, err)
		return
	}
	if req.NodeID == "" || req.Host == "" || req.Port == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "node_id, host y port son requeridos"})
		return
	}
	req.LastSeen = time.Now()
	s.mu.Lock()
	s.nodes[req.NodeID] = &req
	s.mu.Unlock()
	writeJSON(w, http.StatusCreated, map[string]any{"status": "registered", "node_id": req.NodeID})
}

func (s *Server) clusterHeartbeat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var req struct {
		NodeID   string `json:"node_id"`
		Capacity int64  `json:"capacity"`
		Used     int64  `json:"used"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		badRequest(w, err)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	n, ok := s.nodes[req.NodeID]
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "nodo no registrado"})
		return
	}
	n.Capacity = req.Capacity
	n.Used = req.Used
	n.LastSeen = time.Now()
	writeJSON(w, http.StatusOK, map[string]any{"orders": []any{}})
}

func (s *Server) clusterBlockReport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var req struct {
		NodeID string   `json:"node_id"`
		Blocks []string `json:"blocks"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		badRequest(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"accepted": len(req.Blocks)})
}

func (s *Server) notImplemented(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusNotImplemented, map[string]any{"error": "reservado para Hito 3 (replicación de metadatos / elección de líder)"})
}

func randomID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprint(time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}
func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
func methodNotAllowed(w http.ResponseWriter) {
	writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "método no permitido"})
}
func badRequest(w http.ResponseWriter, err error) {
	writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
}
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
