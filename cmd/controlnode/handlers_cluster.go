package main

import (
	"net/http"
	"net/url"
	"strings"
	"time"
)

// health es público: dice qué rol tiene este ControlNode y quién es el líder.
func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	s.op.Lock()
	defer s.op.Unlock()
	addr, id := s.raft.LeaderWithID()
	writeJSON(w, 200, map[string]any{"node_id": s.cfg.NodeID, "role": strings.ToLower(s.raft.State().String()), "leader_id": id, "leader": s.peerHTTP[addr], "applied_index": s.raft.AppliedIndex(), "alive_datanodes": len(s.aliveNodes())})
}
func (s *Server) status(w http.ResponseWriter, r *http.Request) {
	nodes := []map[string]any{}
	for _, n := range s.nodes {
		nodes = append(nodes, map[string]any{"node_id": n.NodeID, "url": n.URL, "capacity": n.Capacity, "used": n.Used, "alive": s.isAlive(n), "verified_blocks": len(n.Blocks)})
	}
	v := s.state()
	files := 0
	for _, f := range v.Files {
		if f.Owner == owner(r) {
			files++
		}
	}
	writeJSON(w, 200, map[string]any{"nodes": nodes, "files": files, "raft": s.raft.Stats(), "replication_factor": s.cfg.ReplicationFactor})
}

// clusterRegister da de alta un DataNode o actualiza su ocupación. Un nodo
// nuevo solo tiene que arrancar y llamar aquí: así crece el clúster.
func (s *Server) clusterRegister(w http.ResponseWriter, r *http.Request) {
	var q NodeInfo
	if !decode(w, r, &q) {
		return
	}
	u, e := url.Parse(q.URL)
	scheme := "https"
	if s.cfg.Security.Insecure {
		scheme = "http"
	}
	if q.NodeID == "" || len(q.NodeID) > 100 || strings.ContainsAny(q.NodeID, "/\\:@") || e != nil || u.Scheme != scheme || u.Host == "" || u.User != nil || u.Path != "" || q.Capacity < 1 || q.Used < 0 || q.Used > q.Capacity {
		fail(w, 400, "registro inválido: id,url,capacity,used")
		return
	}
	if old := s.nodes[q.NodeID]; old != nil {
		q.Blocks = old.Blocks
	}
	q.LastSeen = time.Now()
	s.nodes[q.NodeID] = &q
	writeJSON(w, 201, map[string]any{"status": "registered", "node_id": q.NodeID})
}

// clusterHeartbeat es la señal de vida periódica. Lleva los mismos datos que
// el registro, así que un líder recién elegido conoce al nodo con el primer
// heartbeat que recibe.
func (s *Server) clusterHeartbeat(w http.ResponseWriter, r *http.Request) { s.clusterRegister(w, r) }
func (s *Server) clusterBlockReport(w http.ResponseWriter, r *http.Request) {
	var q struct {
		NodeID string `json:"node_id"`
		Blocks []struct {
			BlockID string `json:"block_id"`
			Size    int64  `json:"size"`
		}
	}
	if !decode(w, r, &q) {
		return
	}
	n := s.nodes[q.NodeID]
	if n == nil {
		fail(w, 404, "registrar primero")
		return
	}
	n.Blocks = map[string]int64{}
	for _, b := range q.Blocks {
		n.Blocks[b.BlockID] = b.Size
	}
	writeJSON(w, 200, map[string]int{"accepted": len(n.Blocks)})
}
