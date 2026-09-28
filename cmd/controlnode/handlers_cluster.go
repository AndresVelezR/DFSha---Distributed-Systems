package main

import (
	"encoding/json"
	"net/http"
	"time"
)

// health resume el estado del ControlNode y cuántos DataNodes ve vivos.
func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	alive := 0
	for _, n := range s.nodes {
		if s.isAlive(n) {
			alive++
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"node_id": s.cfg.NodeID, "role": "leader-hito2", "alive_datanodes": alive, "files": len(s.files)})
}

// clusterRegister da de alta un DataNode. Un nodo nuevo solo tiene que
// arrancar y llamar aquí; no hay que reiniciar nada más.
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

// clusterHeartbeat recibe la señal de vida y la ocupación de un DataNode.
// La respuesta tiene el campo "orders" previsto en el Hito 1 para enviar
// órdenes de copia o borrado; en este hito siempre va vacío (Hito 3).
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

// clusterBlockReport recibe el inventario de bloques de un DataNode. El
// contrato queda fijado; compararlo contra los metadatos es del Hito 3.
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

// notImplemented responde a los endpoints entre ControlNodes, cuyo contrato
// existe desde el Hito 1 pero cuya lógica se implementa en el Hito 3.
func (s *Server) notImplemented(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusNotImplemented, map[string]any{"error": "reservado para Hito 3 (replicación de metadatos / elección de líder)"})
}
