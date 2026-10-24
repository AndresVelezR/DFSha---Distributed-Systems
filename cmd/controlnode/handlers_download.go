package main

import (
	"dfsha/internal/common"
	"encoding/json"
	"net/http"
	"time"
)

func jsonMarshal(v any) (map[string]any, error) {
	b, e := json.Marshal(v)
	if e != nil {
		return nil, e
	}
	var m map[string]any
	e = json.Unmarshal(b, &m)
	return m, e
}
func (s *Server) downloadPlan(w http.ResponseWriter, r *http.Request) {
	p, ok := remote(w, r.URL.Query().Get("path"))
	if !ok {
		return
	}
	u := owner(r)
	v := s.state()
	f := v.Files[key(u, p)]
	if id := r.URL.Query().Get("handle"); id != "" {
		h, e := handleFor(v, u, id, p, "r")
		if e != nil {
			fail(w, 410, e.Error())
			return
		}
		h.ExpiresAt = time.Now().Add(s.cfg.WriteLeaseTTL)
		if !s.persist(w, v) {
			return
		}
		if h.File != nil {
			f = h.File
		}
	}
	if f == nil {
		fail(w, 404, "archivo no existe")
		return
	}
	blocks := []map[string]any{}
	for _, b := range f.Blocks {
		targets := []Target{}
		for _, id := range b.StoredOn {
			n := s.nodes[id]
			if n != nil && s.isAlive(n) {
				targets = append(targets, Target{Node: id, URL: n.URL, Token: common.Sign(common.Claims{Kind: "block-read", User: u, Block: b.BlockID, Node: id, Exp: time.Now().Add(time.Minute).Unix()}, s.cfg.Security.SigningKey)})
			}
		}
		if len(targets) == 0 {
			fail(w, 503, "bloque sin réplicas vivas")
			return
		}
		blocks = append(blocks, map[string]any{"index": b.Index, "block_id": b.BlockID, "size": b.Size, "targets": targets, "replicas": targets})
	}
	writeJSON(w, 200, map[string]any{"path": p, "size": f.Size, "sha256": f.SHA256, "block_size": f.BlockSize, "blocks": blocks})
}
