package main

import (
	"encoding/json"
	"net/http"
	"path"
	"sort"
	"strings"
)

// cleanRemote normaliza una ruta del DFS a forma absoluta y sin "..".
func cleanRemote(p string) string {
	if p == "" {
		return "/"
	}
	return path.Clean("/" + strings.TrimPrefix(p, "/"))
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
		entries = append(entries, map[string]any{"name": rest, "type": "file", "size": f.Size, "blocks": len(f.Blocks), "replicas_ok": s.liveReplicas(f.Blocks)})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i]["name"].(string) < entries[j]["name"].(string) })
	writeJSON(w, http.StatusOK, map[string]any{"path": base, "entries": entries})
}

// liveReplicas devuelve cuántas copias en DataNodes vivos tiene el bloque
// menos replicado del archivo: el archivo es tan redundante como su bloque
// más débil. Debe llamarse con s.mu tomado.
func (s *Server) liveReplicas(blocks []StoredBlock) int {
	lowest := -1
	for _, b := range blocks {
		alive := 0
		for _, id := range b.StoredOn {
			if n, ok := s.nodes[id]; ok && s.isAlive(n) {
				alive++
			}
		}
		if lowest == -1 || alive < lowest {
			lowest = alive
		}
	}
	return max(lowest, 0)
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
	if !s.dirs[path.Dir(p)] {
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

// fsRM borra los metadatos del archivo. Los bloques quedan huérfanos en los
// DataNodes hasta que exista la recolección de basura (Hito 3).
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

// fsMV renombra o mueve un archivo cambiando solo metadatos; los bloques no se tocan.
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
	writeJSON(w, http.StatusOK, map[string]any{"size": f.Size, "block_size": f.BlockSize, "n_blocks": len(f.Blocks), "replicas_ok": s.liveReplicas(f.Blocks), "owner": f.Owner})
}
