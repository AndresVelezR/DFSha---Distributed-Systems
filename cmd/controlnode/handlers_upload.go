package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"path"
	"sort"
	"time"
)

// uploadPlan reserva la ruta y responde con el plan de escritura: tamaño de
// bloque, número de bloques y DataNodes destino de cada bloque. El archivo
// queda "en curso" y no es visible hasta el commit.
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
	nBlocks := numberOfBlocks(req.Size, blockSize)
	rf := replicationFactor
	if rf > len(nodes) {
		rf = len(nodes)
	}
	plans := placeBlocks(nodes, nBlocks, blockSize, rf)

	up := &UploadSession{
		UploadID:  newUploadID(),
		Owner:     demoUser,
		Path:      remote,
		Size:      req.Size,
		BlockSize: blockSize,
		NBlocks:   nBlocks,
		ExpiresAt: time.Now().Add(60 * time.Second),
		Blocks:    plans,
	}
	s.uploads[up.UploadID] = up
	writeJSON(w, http.StatusOK, map[string]any{"upload_id": up.UploadID, "block_size": blockSize, "n_blocks": nBlocks, "lease_expires_at": up.ExpiresAt, "blocks": plans})
}

// uploadCommit publica el archivo cuando el cliente confirma que subió todos
// los bloques. Es el único momento en que el archivo pasa a ser visible.
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

// uploadAbort libera la reserva de una subida que el cliente no va a terminar.
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

func newUploadID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprint(time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}
