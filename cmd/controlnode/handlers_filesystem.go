package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"path"
	"sort"
	"strings"
	"time"
)

func cleanRemote(p string) string {
	if p == "" {
		return "/"
	}
	return path.Clean("/" + strings.TrimPrefix(p, "/"))
}
func validPath(p string) bool {
	return len(p) <= 4096 && !strings.ContainsAny(p, "\\\x00\r\n") && !strings.Contains(p, ":")
}
func remote(w http.ResponseWriter, p string) (string, bool) {
	if !validPath(p) {
		fail(w, 400, "ruta inválida")
		return "", false
	}
	return cleanRemote(p), true
}
func (s *Server) liveReplicas(blocks []StoredBlock) int {
	low := -1
	for _, b := range blocks {
		n := 0
		for _, id := range b.StoredOn {
			if v := s.nodes[id]; v != nil && s.isAlive(v) {
				n++
			}
		}
		if low < 0 || n < low {
			low = n
		}
	}
	return max(low, 0)
}
func (s *Server) fsLS(w http.ResponseWriter, r *http.Request) {
	p, ok := remote(w, r.URL.Query().Get("path"))
	if !ok {
		return
	}
	u := owner(r)
	v := s.state()
	if !v.Dirs[key(u, p)] {
		fail(w, 404, "directorio no existe")
		return
	}
	entries := []map[string]any{}
	prefix := strings.TrimSuffix(p, "/") + "/"
	for k := range v.Dirs {
		if !strings.HasPrefix(k, u+":") {
			continue
		}
		d := strings.TrimPrefix(k, u+":")
		if d != p && path.Dir(d) == p {
			entries = append(entries, map[string]any{"name": strings.TrimPrefix(d, prefix), "type": "dir", "mode": "0700"})
		}
	}
	for _, f := range v.Files {
		if f.Owner == u && path.Dir(f.Path) == p {
			entries = append(entries, map[string]any{"name": path.Base(f.Path), "type": "file", "size": f.Size, "blocks": len(f.Blocks), "replicas_ok": s.liveReplicas(f.Blocks), "mode": "0600"})
		}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i]["name"].(string) < entries[j]["name"].(string) })
	writeJSON(w, 200, map[string]any{"path": p, "entries": entries})
}
func (s *Server) fsMkdir(w http.ResponseWriter, r *http.Request) {
	var q struct{ Path string }
	if !decode(w, r, &q) {
		return
	}
	p, ok := remote(w, q.Path)
	if !ok {
		return
	}
	u := owner(r)
	v := s.state()
	if v.Dirs[key(u, p)] || v.Files[key(u, p)] != nil {
		fail(w, 409, "ruta ya existe")
		return
	}
	if !v.Dirs[key(u, path.Dir(p))] {
		fail(w, 404, "padre no existe")
		return
	}
	if reserved(v, u, p, "") {
		fail(w, 409, "ruta reservada")
		return
	}
	v.Dirs[key(u, p)] = true
	if s.persist(w, v) {
		writeJSON(w, 201, map[string]string{"path": p})
	}
}
func reserved(v *State, u, p, except string) bool {
	now := time.Now()
	for _, up := range v.Uploads {
		if up.Owner == u && up.Path == p && now.Before(up.ExpiresAt) {
			return true
		}
	}
	for id, h := range v.Handles {
		if id != except && h.Owner == u && h.Path == p && h.Mode == "w" && now.Before(h.ExpiresAt) {
			return true
		}
	}
	return false
}
func (s *Server) fsRmdir(w http.ResponseWriter, r *http.Request) {
	p, ok := remote(w, r.URL.Query().Get("path"))
	if !ok {
		return
	}
	u := owner(r)
	v := s.state()
	if p == "/" {
		fail(w, 409, "raíz protegida")
		return
	}
	if !v.Dirs[key(u, p)] {
		fail(w, 404, "directorio no existe")
		return
	}
	prefix := key(u, p) + "/"
	for k := range v.Dirs {
		if strings.HasPrefix(k, prefix) {
			fail(w, 409, "directorio no vacío")
			return
		}
	}
	for k := range v.Files {
		if strings.HasPrefix(k, prefix) {
			fail(w, 409, "directorio no vacío")
			return
		}
	}
	for _, up := range v.Uploads {
		if up.Owner == u && strings.HasPrefix(up.Path, p+"/") && time.Now().Before(up.ExpiresAt) {
			fail(w, 409, "subida activa")
			return
		}
	}
	for _, h := range v.Handles {
		if h.Owner == u && strings.HasPrefix(h.Path, p+"/") && time.Now().Before(h.ExpiresAt) {
			fail(w, 409, "acceso activo")
			return
		}
	}
	delete(v.Dirs, key(u, p))
	if s.persist(w, v) {
		w.WriteHeader(204)
	}
}
func (s *Server) fsRM(w http.ResponseWriter, r *http.Request) {
	p, ok := remote(w, r.URL.Query().Get("path"))
	if !ok {
		return
	}
	u := owner(r)
	v := s.state()
	if v.Files[key(u, p)] == nil {
		fail(w, 404, "archivo no existe")
		return
	}
	if reserved(v, u, p, "") {
		fail(w, 409, "escritor activo")
		return
	}
	delete(v.Files, key(u, p))
	if s.persist(w, v) {
		w.WriteHeader(204)
	}
}
func (s *Server) fsMV(w http.ResponseWriter, r *http.Request) {
	var q struct{ Src, Dst string }
	if !decode(w, r, &q) {
		return
	}
	src, ok := remote(w, q.Src)
	if !ok {
		return
	}
	dst, ok := remote(w, q.Dst)
	if !ok {
		return
	}
	u := owner(r)
	v := s.state()
	if src == "/" || dst == "/" || strings.HasPrefix(dst, src+"/") {
		fail(w, 409, "movimiento inválido")
		return
	}
	if v.Files[key(u, dst)] != nil || v.Dirs[key(u, dst)] {
		fail(w, 409, "destino existe")
		return
	}
	if !v.Dirs[key(u, path.Dir(dst))] {
		fail(w, 404, "padre destino no existe")
		return
	}
	isDir := v.Dirs[key(u, src)]
	if !isDir && v.Files[key(u, src)] == nil {
		fail(w, 404, "origen no existe")
		return
	}
	for _, up := range v.Uploads {
		if up.Owner == u && time.Now().Before(up.ExpiresAt) && (up.Path == src || strings.HasPrefix(up.Path, src+"/") || up.Path == dst) {
			fail(w, 409, "subida activa")
			return
		}
	}
	for _, h := range v.Handles {
		if h.Owner == u && h.Mode == "w" && time.Now().Before(h.ExpiresAt) && (h.Path == src || strings.HasPrefix(h.Path, src+"/") || h.Path == dst) {
			fail(w, 409, "escritor activo")
			return
		}
	}
	if isDir {
		dirs := map[string]bool{}
		for k, b := range v.Dirs {
			if k == key(u, src) || strings.HasPrefix(k, key(u, src)+"/") {
				dirs[key(u, dst)+strings.TrimPrefix(k, key(u, src))] = b
				delete(v.Dirs, k)
			}
		}
		for k, b := range dirs {
			v.Dirs[k] = b
		}
	}
	moved := map[string]*FileMeta{}
	for k, f := range v.Files {
		if f.Owner == u && (f.Path == src || isDir && strings.HasPrefix(f.Path, src+"/")) {
			f.Path = dst + strings.TrimPrefix(f.Path, src)
			moved[key(u, f.Path)] = f
			delete(v.Files, k)
		}
	}
	for k, f := range moved {
		v.Files[k] = f
	}
	if s.persist(w, v) {
		writeJSON(w, 200, map[string]string{"src": src, "dst": dst})
	}
}
func (s *Server) fsStat(w http.ResponseWriter, r *http.Request) {
	p, ok := remote(w, r.URL.Query().Get("path"))
	if !ok {
		return
	}
	u := owner(r)
	v := s.state()
	if v.Dirs[key(u, p)] {
		writeJSON(w, 200, map[string]any{"path": p, "type": "dir", "owner": u, "mode": "0700"})
		return
	}
	f := v.Files[key(u, p)]
	if f == nil {
		fail(w, 404, "archivo no existe")
		return
	}
	b, _ := toMap(f)
	b["type"] = "file"
	b["mode"] = "0600"
	b["n_blocks"] = len(f.Blocks)
	b["replicas_ok"] = s.liveReplicas(f.Blocks)
	writeJSON(w, 200, b)
}
func toMap(v any) (map[string]any, error) {
	b, e := json.Marshal(v)
	if e != nil {
		return nil, e
	}
	var m map[string]any
	e = json.Unmarshal(b, &m)
	return m, e
}

func (s *Server) openFile(w http.ResponseWriter, r *http.Request) { s.doOpen(w, r, false) }
func (s *Server) lockFile(w http.ResponseWriter, r *http.Request) { s.doOpen(w, r, true) }
func (s *Server) doOpen(w http.ResponseWriter, r *http.Request, lock bool) {
	var q struct{ Path, Mode string }
	if !decode(w, r, &q) {
		return
	}
	if lock {
		q.Mode = "w"
	}
	if q.Mode == "" {
		q.Mode = "r"
	}
	if q.Mode != "r" && q.Mode != "w" {
		fail(w, 400, "mode r o w")
		return
	}
	p, ok := remote(w, q.Path)
	if !ok {
		return
	}
	u := owner(r)
	v := s.state()
	if v.Dirs[key(u, p)] {
		fail(w, 409, "es un directorio")
		return
	}
	f := v.Files[key(u, p)]
	if q.Mode == "r" && f == nil {
		fail(w, 404, "archivo no existe")
		return
	}
	if !v.Dirs[key(u, path.Dir(p))] {
		fail(w, 404, "padre no existe")
		return
	}
	if q.Mode == "w" && reserved(v, u, p, "") {
		fail(w, 409, "reserva activa")
		return
	}
	h := &Handle{ID: newUploadID(), Owner: u, Path: p, Mode: q.Mode, ExpiresAt: time.Now().Add(s.cfg.WriteLeaseTTL), File: f}
	v.Handles[h.ID] = h
	if s.persist(w, v) {
		writeJSON(w, 200, h)
	}
}
func (s *Server) closeFile(w http.ResponseWriter, r *http.Request) {
	var q struct{ Handle string }
	if !decode(w, r, &q) {
		return
	}
	v := s.state()
	h := v.Handles[q.Handle]
	if h == nil {
		w.WriteHeader(204)
		return
	}
	if h.Owner != owner(r) {
		fail(w, 403, "handle de otro usuario")
		return
	}
	for _, up := range v.Uploads {
		if up.Handle == q.Handle && time.Now().Before(up.ExpiresAt) {
			fail(w, 409, "subida todavía activa")
			return
		}
	}
	delete(v.Handles, q.Handle)
	if s.persist(w, v) {
		w.WriteHeader(204)
	}
}
func handleFor(v *State, u, id, p, mode string) (*Handle, error) {
	h := v.Handles[id]
	if h == nil || h.Owner != u || h.Path != p || time.Now().After(h.ExpiresAt) {
		return nil, fmt.Errorf("handle inválido o vencido")
	}
	if mode == "w" && h.Mode != "w" {
		return nil, fmt.Errorf("handle no permite escribir")
	}
	return h, nil
}

func (s *Server) renewHandle(w http.ResponseWriter, r *http.Request) {
	var q struct{ Handle string }
	if !decode(w, r, &q) {
		return
	}
	v := s.state()
	h := v.Handles[q.Handle]
	if h == nil || h.Owner != owner(r) || time.Now().After(h.ExpiresAt) {
		fail(w, 410, "handle inválido o vencido")
		return
	}
	h.ExpiresAt = time.Now().Add(s.cfg.WriteLeaseTTL)
	if s.persist(w, v) {
		writeJSON(w, 200, h)
	}
}
