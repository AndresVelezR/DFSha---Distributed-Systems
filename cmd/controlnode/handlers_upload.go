package main

import (
	"crypto/rand"
	"dfsha/internal/common"
	"encoding/hex"
	"fmt"
	"net/http"
	"path"
	"sort"
	"strconv"
	"time"
)

func newUploadID() string {
	b := make([]byte, 16)
	if _, e := rand.Read(b); e != nil {
		panic(e)
	}
	return hex.EncodeToString(b)
}
func (s *Server) decorate(up *UploadSession) {
	for i := range up.Blocks {
		for j := range up.Blocks[i].Targets {
			t := &up.Blocks[i].Targets[j]
			t.Token = common.Sign(common.Claims{Kind: "block-write", User: up.Owner, Block: "*", Node: t.Node, Exp: up.ExpiresAt.Unix(), MaxSize: up.BlockSize}, s.cfg.Security.SigningKey)
		}
	}
}
func (s *Server) uploadPlan(w http.ResponseWriter, r *http.Request) {
	var q struct {
		Path   string
		Size   int64
		SHA256 string
		Handle string
	}
	if !decode(w, r, &q) {
		return
	}
	p, ok := remote(w, q.Path)
	if !ok {
		return
	}
	if q.Size < 0 || !common.ValidID(q.SHA256) {
		fail(w, 400, "size>=0 y sha256 hexadecimal obligatorios")
		return
	}
	u := owner(r)
	v := s.state()
	if !v.Dirs[key(u, path.Dir(p))] {
		fail(w, 404, "padre no existe")
		return
	}
	if v.Dirs[key(u, p)] {
		fail(w, 409, "ruta es directorio")
		return
	}
	if q.Handle != "" {
		h, e := handleFor(v, u, q.Handle, p, "w")
		if e != nil {
			fail(w, 410, e.Error())
			return
		}
		h.ExpiresAt = time.Now().Add(s.cfg.WriteLeaseTTL)
	}
	if reserved(v, u, p, q.Handle) {
		fail(w, 409, "reserva activa para ruta")
		return
	}
	nodes := s.aliveNodes()
	if len(nodes) == 0 {
		fail(w, 503, "no hay DataNodes vivos")
		return
	}
	bs := chooseBlockSize(q.Size, len(nodes), s.cfg.BlockSizing)
	count := numberOfBlocks(q.Size, bs)
	if count > 100000 {
		fail(w, 413, "demasiados bloques para plano de control académico")
		return
	}
	target, copies := copiesPerBlock(s.cfg.ReplicationFactor, len(s.nodes), len(nodes))
	_ = target
	// Reserve capacity of other in-flight plans as well as bytes already on disk.
	for _, up := range v.Uploads {
		if time.Now().Before(up.ExpiresAt) {
			for _, b := range up.Blocks {
				for _, t := range b.Targets {
					for _, n := range nodes {
						if n.NodeID == t.Node {
							n.Used += up.BlockSize
						}
					}
				}
			}
		}
	}
	plans := make([]BlockPlan, count)
	virtual := map[string]int64{}
	for _, n := range nodes {
		virtual[n.NodeID] = n.Used
	}
	for i := range plans {
		size := min(bs, max(q.Size-int64(i)*bs, 0))
		sort.Slice(nodes, func(a, b int) bool {
			ra := float64(virtual[nodes[a].NodeID]) / float64(nodes[a].Capacity)
			rb := float64(virtual[nodes[b].NodeID]) / float64(nodes[b].Capacity)
			if ra == rb {
				return nodes[a].NodeID < nodes[b].NodeID
			}
			return ra < rb
		})
		plans[i] = BlockPlan{Index: i, Targets: []Target{}}
		for _, n := range nodes {
			if float64(virtual[n.NodeID]+size+32) > float64(n.Capacity)*s.cfg.FullThreshold {
				continue
			}
			plans[i].Targets = append(plans[i].Targets, Target{Node: n.NodeID, URL: n.URL})
			virtual[n.NodeID] += size + 32
			if len(plans[i].Targets) == copies {
				break
			}
		}
		if len(plans[i].Targets) < copies {
			fail(w, 507, "capacidad insuficiente por nodo o umbral de ocupación")
			return
		}
	}
	up := &UploadSession{UploadID: newUploadID(), Owner: u, Path: p, Size: q.Size, SHA256: q.SHA256, BlockSize: bs, NBlocks: count, ExpiresAt: time.Now().Add(s.cfg.WriteLeaseTTL), Blocks: plans, Handle: q.Handle}
	v.Uploads[up.UploadID] = up
	if !s.persist(w, v) {
		return
	}
	s.decorate(up)
	writeJSON(w, 200, up)
}
func (s *Server) uploadRenew(w http.ResponseWriter, r *http.Request) {
	var q struct {
		UploadID string `json:"upload_id"`
	}
	if !decode(w, r, &q) {
		return
	}
	v := s.state()
	up := v.Uploads[q.UploadID]
	if up == nil || up.Owner != owner(r) || time.Now().After(up.ExpiresAt) {
		fail(w, 410, "reserva vencida")
		return
	}
	up.ExpiresAt = time.Now().Add(s.cfg.WriteLeaseTTL)
	if h := v.Handles[up.Handle]; h != nil {
		h.ExpiresAt = up.ExpiresAt
	}
	if !s.persist(w, v) {
		return
	}
	s.decorate(up)
	writeJSON(w, 200, up)
}
func (s *Server) verifyBlock(n *NodeInfo, b StoredBlock, blockOwner ...string) bool {
	req, _ := http.NewRequest("HEAD", n.URL+"/blocks/"+b.BlockID, nil)
	req.Header.Set("X-Cluster-Key", s.cfg.Security.ClusterKey)
	if len(blockOwner) > 0 {
		req.Header.Set("X-Block-Owner", blockOwner[0])
	}
	resp, e := s.client.Do(req)
	if e != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == 200 && resp.Header.Get("X-Block-SHA256") == b.BlockID && resp.Header.Get("Content-Length") == strconv.FormatInt(b.Size, 10)
}
func (s *Server) uploadCommit(w http.ResponseWriter, r *http.Request) {
	var q struct {
		UploadID string        `json:"upload_id"`
		Blocks   []StoredBlock `json:"blocks"`
		SHA256   string        `json:"sha256"`
	}
	if !decode(w, r, &q) {
		return
	}
	u := owner(r)
	v := s.state()
	if f := v.Completed[q.UploadID]; f != nil {
		if f.Owner != u {
			fail(w, 403, "subida de otro usuario")
			return
		}
		if q.SHA256 != f.SHA256 {
			fail(w, 409, "commit distinto")
			return
		}
		writeJSON(w, 200, map[string]any{"status": "publicado", "path": f.Path, "idempotent": true})
		return
	}
	up := v.Uploads[q.UploadID]
	if up == nil || up.Owner != u || time.Now().After(up.ExpiresAt) {
		fail(w, 410, "reserva inválida o vencida")
		return
	}
	if len(q.Blocks) != up.NBlocks || q.SHA256 != up.SHA256 {
		fail(w, 409, "tamaño de manifiesto o SHA inválido")
		return
	}
	sort.Slice(q.Blocks, func(i, j int) bool { return q.Blocks[i].Index < q.Blocks[j].Index })
	for i := range q.Blocks {
		b := &q.Blocks[i]
		expected := min(up.BlockSize, max(up.Size-int64(i)*up.BlockSize, 0))
		if b.Index != i || b.Size != expected || !common.ValidID(b.BlockID) {
			fail(w, 409, fmt.Sprintf("bloque%d inválido", i))
			return
		}
		verified := []string{}
		for _, t := range up.Blocks[i].Targets {
			n := s.nodes[t.Node]
			if n != nil && s.isAlive(n) && s.verifyBlock(n, *b, u) {
				verified = append(verified, t.Node)
			}
		}
		if len(verified) != len(up.Blocks[i].Targets) {
			fail(w, 409, "faltan réplicas verificadas del plan")
			return
		}
		b.StoredOn = verified
	}
	if time.Now().After(up.ExpiresAt) {
		fail(w, 410, "reserva vencida durante verificación")
		return
	}
	f := &FileMeta{Path: up.Path, Owner: u, Size: up.Size, SHA256: up.SHA256, BlockSize: up.BlockSize, Blocks: q.Blocks, CreatedAt: time.Now()}
	v.Files[key(u, up.Path)] = f
	v.Completed[q.UploadID] = f
	delete(v.Uploads, q.UploadID)
	if h := v.Handles[up.Handle]; h != nil {
		h.File = f
		h.ExpiresAt = time.Now().Add(s.cfg.WriteLeaseTTL)
	}
	if s.persist(w, v) {
		writeJSON(w, 200, map[string]any{"status": "publicado", "path": up.Path, "replicas_ok": s.liveReplicas(f.Blocks)})
	}
}
func (s *Server) uploadAbort(w http.ResponseWriter, r *http.Request) {
	var q struct {
		UploadID string `json:"upload_id"`
	}
	if !decode(w, r, &q) {
		return
	}
	v := s.state()
	up := v.Uploads[q.UploadID]
	if up == nil {
		w.WriteHeader(204)
		return
	}
	if up.Owner != owner(r) {
		fail(w, 403, "subida de otro usuario")
		return
	}
	delete(v.Uploads, q.UploadID)
	if s.persist(w, v) {
		w.WriteHeader(204)
	}
}
