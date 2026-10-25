package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"path"
	"sort"
	"strconv"
	"time"

	"dfsha/internal/common"
)

func newUploadID() string {
	b := make([]byte, 16)
	if _, e := rand.Read(b); e != nil {
		panic(e)
	}
	return hex.EncodeToString(b)
}

// decorate agrega a cada destino del plan un permiso de escritura firmado,
// válido solo en ese DataNode, hasta que venza la reserva y para bloques de
// hasta block_size. Con él el cliente escribe en el DataNode sin que este
// tenga que consultar al ControlNode.
func (s *Server) decorate(up *UploadSession) {
	for i := range up.Blocks {
		for j := range up.Blocks[i].Targets {
			t := &up.Blocks[i].Targets[j]
			t.Token = common.Sign(common.Claims{Kind: "block-write", User: up.Owner, Block: "*", Node: t.Node, Exp: up.ExpiresAt.Unix(), MaxSize: up.BlockSize}, s.cfg.Security.SigningKey)
		}
	}
}

// uploadPlan es el inicio de una subida (open + lock del Hito 1): valida la
// ruta, reserva el archivo para un solo escritor, calcula el tamaño de bloque
// y decide en qué DataNodes va cada copia. El archivo todavía no existe para
// nadie: solo aparece con el commit.
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
	sizes := blockSizes(q.Size, bs)
	if len(sizes) > maxBlocksPerFile {
		fail(w, 413, "el archivo produce demasiados bloques")
		return
	}
	_, copies := copiesPerBlock(s.cfg.ReplicationFactor, len(s.nodes), len(nodes))
	// Las subidas en curso todavía no aparecen en el disco de los DataNodes:
	// se suman a su ocupación para no prometer dos veces el mismo espacio.
	for _, other := range v.Uploads {
		if time.Now().Before(other.ExpiresAt) {
			for _, b := range other.Blocks {
				for _, t := range b.Targets {
					for _, n := range nodes {
						if n.NodeID == t.Node {
							n.Used += other.BlockSize
						}
					}
				}
			}
		}
	}
	plans, ok := placeBlocks(nodes, sizes, copies, s.cfg.FullThreshold)
	if !ok {
		fail(w, 507, "no hay espacio suficiente en los DataNodes")
		return
	}
	up := &UploadSession{UploadID: newUploadID(), Owner: u, Path: p, Size: q.Size, SHA256: q.SHA256, BlockSize: bs, NBlocks: len(sizes), ExpiresAt: time.Now().Add(s.cfg.WriteLeaseTTL), Blocks: plans, Handle: q.Handle}
	v.Uploads[up.UploadID] = up
	if !s.persist(w, v) {
		return
	}
	s.decorate(up)
	writeJSON(w, 200, up)
}

// uploadRenew extiende la reserva mientras el cliente sigue subiendo. Si el
// cliente muere deja de renovarla y la reserva vence sola.
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

// verifyBlock le pregunta al DataNode, con un HEAD, si tiene el bloque íntegro
// y del tamaño esperado. Si se indica blockOwner, además exige que esa cuenta
// haya subido el bloque a ese nodo.
func (s *Server) verifyBlock(n *NodeInfo, b StoredBlock, blockOwner ...string) bool {
	headers := map[string]string{}
	if len(blockOwner) > 0 {
		headers["X-Block-Owner"] = blockOwner[0]
	}
	resp, e := s.internal(s.quickClient, "HEAD", n.URL+"/blocks/"+b.BlockID, nil, headers)
	if e != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == 200 && resp.Header.Get("X-Block-SHA256") == b.BlockID && resp.Header.Get("Content-Length") == strconv.FormatInt(b.Size, 10)
}

// uploadCommit publica el archivo. Antes de confiar en lo que dice el
// cliente, pregunta a cada DataNode del plan si tiene la copia íntegra y si
// fue esta cuenta quien la subió. Solo si todas están, publica el manifiesto
// por Raft. Repetir el commit con el mismo upload_id es seguro.
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
	expected := blockSizes(up.Size, up.BlockSize)
	for i := range q.Blocks {
		b := &q.Blocks[i]
		if b.Index != i || b.Size != expected[i] || !common.ValidID(b.BlockID) {
			fail(w, 409, fmt.Sprintf("bloque %d inválido", i))
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

// uploadAbort libera la reserva. Los bloques ya subidos quedan huérfanos y
// los borra el mantenimiento.
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
