package main

import "net/http"

// downloadPlan devuelve, para cada bloque del archivo, las réplicas que están
// en DataNodes vivos. El cliente descarga de la primera y, si falla, pasa a la
// siguiente.
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
			if n, ok := s.nodes[id]; ok && isAlive(n) {
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
