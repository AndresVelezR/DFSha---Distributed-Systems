package main

import (
	"bytes"
	"encoding/json"
	"log"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/hashicorp/raft"
)

// Mantenimiento: cada MAINTENANCE_MS el líder revisa el clúster en este orden.
//  1. Vence reservas de subida y handles abandonados.
//  2. Pide a cada DataNode vivo su inventario real de bloques.
//  3. Repara: si un bloque tiene menos copias que las deseadas, copia uno
//     entre DataNodes. Si tiene de más, retira las sobrantes.
//  4. Rebalancea: si la ocupación entre el nodo más lleno y el más vacío
//     difiere más que REBALANCE_THRESHOLD_PERCENT, mueve un bloque.
//  5. Publica el mapa nuevo por Raft y solo después borra copias retiradas
//     y bloques huérfanos que ya cumplieron GC_GRACE_SECONDS.
//
// Cada pasada mueve como máximo un bloque, para acotar el tráfico de fondo.

func (s *Server) maintenanceLoop() {
	tick := time.NewTicker(s.cfg.MaintenanceInterval)
	defer tick.Stop()
	for range tick.C {
		s.op.Lock()
		if s.raft.State() == raft.Leader && s.raft.VerifyLeader().Error() == nil && s.raft.Barrier(3*time.Second).Error() == nil {
			s.maintain()
		}
		s.op.Unlock()
	}
}

func (s *Server) maintain() {
	v := s.state()
	changed, uploading := expireLeases(v, time.Now())
	nodes := s.refreshInventories()
	desired, _ := copiesPerBlock(s.cfg.ReplicationFactor, len(s.nodes), len(nodes))
	desired = min(desired, len(nodes))

	repaired, moved := s.repairReplicas(v, nodes, desired, uploading)
	changed = changed || repaired
	if !moved && !uploading && s.rebalanceOne(v, nodes, desired) {
		changed = true
	}
	if changed {
		if e := s.save(v); e != nil {
			log.Printf("mantenimiento: no se pudo publicar el mapa: %v", e)
			return
		}
	}
	// Mientras haya una subida en curso no se conocen todavía sus bloques,
	// así que no se borra nada: podría parecer basura un bloque recién subido.
	if uploading {
		return
	}
	refs := referencedBlocks(v)
	s.deleteRetired(refs, nodes, desired)
	s.collectOrphans(refs, nodes)
}

// expireLeases borra reservas y handles vencidos. Devuelve si cambió el
// estado y si queda alguna subida en curso.
func expireLeases(v *State, now time.Time) (changed, uploading bool) {
	for id, up := range v.Uploads {
		if now.After(up.ExpiresAt) {
			delete(v.Uploads, id)
			changed = true
		} else {
			uploading = true
		}
	}
	for id, h := range v.Handles {
		if now.After(h.ExpiresAt) {
			delete(v.Handles, id)
			changed = true
		}
	}
	return changed, uploading
}

// refreshInventories consulta el inventario de los DataNodes vivos y devuelve
// los que respondieron. Solo sobre ellos se toman decisiones de copia.
func (s *Server) refreshInventories() []*NodeInfo {
	verified := []*NodeInfo{}
	for _, alive := range s.aliveNodes() {
		n := s.nodes[alive.NodeID]
		if s.inventory(n) {
			verified = append(verified, n)
		}
	}
	return verified
}

func (s *Server) inventory(n *NodeInfo) bool {
	resp, e := s.internal(s.quickClient, "GET", n.URL+"/inventory", nil, nil)
	if e != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return false
	}
	var q struct {
		Used     int64 `json:"used"`
		Capacity int64 `json:"capacity"`
		Blocks   []struct {
			BlockID string `json:"block_id"`
			Size    int64  `json:"size"`
		} `json:"blocks"`
	}
	if json.NewDecoder(resp.Body).Decode(&q) != nil {
		return false
	}
	n.Blocks = map[string]int64{}
	for _, b := range q.Blocks {
		n.Blocks[b.BlockID] = b.Size
	}
	n.Used = q.Used
	n.Capacity = q.Capacity
	return true
}

// referencedBlocks son los bloques que alguien puede necesitar: los de los
// archivos publicados y los de versiones que un handle abierto todavía lee.
func referencedBlocks(v *State) map[string]StoredBlock {
	refs := map[string]StoredBlock{}
	eachBlock(v, func(b *StoredBlock) { refs[b.BlockID] = *b })
	return refs
}

func eachBlock(v *State, fn func(*StoredBlock)) {
	for _, f := range v.Files {
		for i := range f.Blocks {
			fn(&f.Blocks[i])
		}
	}
	for _, h := range v.Handles {
		if h.File != nil && time.Now().Before(h.ExpiresAt) {
			for i := range h.File.Blocks {
				fn(&h.File.Blocks[i])
			}
		}
	}
}

// repairReplicas compara, bloque por bloque, cuántas copias válidas hay con
// cuántas se desean. Repone una copia faltante o retira las sobrantes, y deja
// en los metadatos dónde está realmente cada bloque.
func (s *Server) repairReplicas(v *State, nodes []*NodeInfo, desired int, uploading bool) (changed, moved bool) {
	now := time.Now()
	for id, b := range referencedBlocks(v) {
		holders, retired := s.holdersOf(id, b.Size, nodes)
		// Una copia retirada que todavía existe vuelve a contar si hace falta.
		for _, n := range retired {
			if len(holders) >= desired {
				break
			}
			delete(s.retired, n.NodeID+":"+id)
			holders = append(holders, n)
		}
		if len(holders) > desired && desired > 0 && !uploading {
			sortByOccupancy(holders, func(n *NodeInfo) int64 { return n.Used })
			for _, n := range holders[desired:] {
				s.retire(n.NodeID, id, now)
			}
			holders = holders[:desired]
			moved = true
		}
		if len(holders) == 0 {
			continue
		}
		if len(holders) < desired && !moved {
			if dst := s.repairTarget(id, b.Size, nodes); dst != nil && s.copyBlock(holders[0], dst, b) {
				holders = append(holders, dst)
				moved = true
				log.Printf("reparación bloque=%s destino=%s", id, dst.NodeID)
			}
		}
		ids := make([]string, 0, len(holders))
		for _, n := range holders {
			ids = append(ids, n.NodeID)
		}
		eachBlock(v, func(x *StoredBlock) {
			if x.BlockID == id && !sameIDs(x.StoredOn, ids) {
				x.StoredOn = append([]string{}, ids...)
				changed = true
			}
		})
	}
	return changed, moved
}

// holdersOf devuelve los nodos que tienen el bloque con el tamaño correcto,
// separando las copias activas de las que están retiradas esperando borrado.
func (s *Server) holdersOf(id string, size int64, nodes []*NodeInfo) (active, retired []*NodeInfo) {
	for _, n := range nodes {
		if z, ok := n.Blocks[id]; ok && z == size {
			if _, r := s.retired[n.NodeID+":"+id]; r {
				retired = append(retired, n)
			} else {
				active = append(active, n)
			}
		}
	}
	return active, retired
}

// repairTarget elige el nodo menos ocupado que no tiene el bloque y que lo
// puede recibir sin pasar el umbral de ocupación.
func (s *Server) repairTarget(id string, size int64, nodes []*NodeInfo) *NodeInfo {
	candidates := append([]*NodeInfo(nil), nodes...)
	sortByOccupancy(candidates, func(n *NodeInfo) int64 { return n.Used })
	for _, n := range candidates {
		if _, has := n.Blocks[id]; !has && s.fits(n, size) {
			return n
		}
	}
	return nil
}

// rebalanceOne mueve un bloque del nodo más ocupado al menos ocupado si la
// diferencia supera el umbral. Primero copia y verifica; la copia vieja queda
// retirada y se borra después, nunca antes de publicar el mapa nuevo.
func (s *Server) rebalanceOne(v *State, nodes []*NodeInfo, desired int) bool {
	if len(nodes) < 2 {
		return false
	}
	sorted := append([]*NodeInfo(nil), nodes...)
	sortByOccupancy(sorted, s.balanceUsed)
	lo, hi := sorted[0], sorted[len(sorted)-1]
	gap := s.occupancy(hi, s.balanceUsed(hi)) - s.occupancy(lo, s.balanceUsed(lo))
	if gap <= s.cfg.RebalanceThreshold {
		return false
	}
	for id, b := range referencedBlocks(v) {
		_, onHi := hi.Blocks[id]
		_, onLo := lo.Blocks[id]
		nextGap := s.occupancy(hi, s.balanceUsed(hi)-b.Size) - s.occupancy(lo, s.balanceUsed(lo)+b.Size)
		if !onHi || onLo || !contains(b.StoredOn, hi.NodeID) || len(b.StoredOn) < desired ||
			!s.fits(lo, b.Size) || nextGap < 0 || nextGap >= gap {
			continue
		}
		if !s.copyBlock(hi, lo, b) {
			continue
		}
		eachBlock(v, func(x *StoredBlock) {
			if x.BlockID == id {
				ids := []string{}
				for _, n := range x.StoredOn {
					if n != hi.NodeID {
						ids = append(ids, n)
					}
				}
				x.StoredOn = append(ids, lo.NodeID)
			}
		})
		s.retire(hi.NodeID, id, time.Now())
		log.Printf("rebalanceo bloque=%s origen=%s destino=%s", id, hi.NodeID, lo.NodeID)
		return true
	}
	return false
}

// deleteRetired borra las copias retiradas cuyo plazo de gracia ya pasó, solo
// si el bloque sigue teniendo suficientes copias válidas en otros nodos.
func (s *Server) deleteRetired(refs map[string]StoredBlock, nodes []*NodeInfo, desired int) {
	now := time.Now()
	for k, d := range s.retired {
		if now.Before(d.At) {
			continue
		}
		n := s.nodes[d.Node]
		if n == nil || !s.isAlive(n) {
			continue
		}
		if b, used := refs[d.Block]; used {
			others := 0
			for _, other := range nodes {
				if size, ok := other.Blocks[d.Block]; ok && size == b.Size && other.NodeID != n.NodeID {
					others++
				}
			}
			if others < max(desired, 1) {
				continue
			}
		}
		if s.raft.VerifyLeader().Error() != nil {
			return
		}
		if s.deleteBlock(n, d.Block) {
			delete(s.retired, k)
		}
	}
}

// collectOrphans borra bloques que ningún archivo referencia: los de archivos
// eliminados o subidas abortadas. Espera GC_GRACE_SECONDS desde que el líder
// los ve huérfanos (la cuenta vive en su memoria, así que un líder nuevo
// empieza de cero) y desde que arrancó el proceso, para no borrar algo que un
// cliente todavía puede estar usando con un permiso ya emitido.
func (s *Server) collectOrphans(refs map[string]StoredBlock, nodes []*NodeInfo) {
	now := time.Now()
	for _, n := range nodes {
		for id := range n.Blocks {
			key := n.NodeID + ":" + id
			if _, used := refs[id]; used {
				delete(s.orphanSince, key)
				continue
			}
			first, seen := s.orphanSince[key]
			if !seen {
				s.orphanSince[key] = now
				continue
			}
			if now.Sub(first) < s.cfg.GCGrace || time.Since(s.started) < s.cfg.GCGrace {
				continue
			}
			if s.raft.VerifyLeader().Error() != nil {
				return
			}
			if s.deleteBlock(n, id) {
				delete(s.orphanSince, key)
			}
		}
	}
}

// copyBlock le ordena al DataNode src que copie el bloque a dst, y confirma
// con dst que la copia quedó íntegra antes de contarla.
func (s *Server) copyBlock(src, dst *NodeInfo, b StoredBlock) bool {
	resp, e := s.internal(s.client, "POST", src.URL+"/blocks/"+b.BlockID+"/replicate", map[string]any{"target": Target{Node: dst.NodeID, URL: dst.URL}}, nil)
	if e != nil {
		return false
	}
	resp.Body.Close()
	if resp.StatusCode >= 300 || !s.verifyBlock(dst, b) {
		return false
	}
	if dst.Blocks == nil {
		dst.Blocks = map[string]int64{}
	}
	dst.Blocks[b.BlockID] = b.Size
	dst.Used += b.Size
	return true
}

func (s *Server) deleteBlock(n *NodeInfo, id string) bool {
	resp, e := s.internal(s.quickClient, "DELETE", n.URL+"/blocks/"+id, nil, nil)
	if e != nil {
		return false
	}
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		return false
	}
	delete(n.Blocks, id)
	return true
}

// internal hace una petición entre nodos autenticada con la clave del clúster.
func (s *Server) internal(client *http.Client, method, url string, body any, headers map[string]string) (*http.Response, error) {
	var b bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&b).Encode(body)
	}
	req, e := http.NewRequest(method, url, &b)
	if e != nil {
		return nil, e
	}
	req.Header.Set("X-Cluster-Key", s.cfg.Security.ClusterKey)
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return client.Do(req)
}

func (s *Server) retire(node, block string, now time.Time) {
	s.retired[node+":"+block] = retiredReplica{Node: node, Block: block, At: now.Add(s.cfg.GCGrace)}
}

// balanceUsed es la ocupación que cuenta para rebalancear: descuenta las
// copias retiradas, que todavía ocupan disco pero ya no son ubicaciones
// activas. Sin esto, el nodo de origen seguiría pareciendo lleno y el
// rebalanceo movería bloques de ida y vuelta.
func (s *Server) balanceUsed(n *NodeInfo) int64 {
	used := n.Used
	for _, retired := range s.retired {
		if retired.Node == n.NodeID {
			if size, exists := n.Blocks[retired.Block]; exists {
				used -= size
			}
		}
	}
	return max(used, 0)
}

func (s *Server) occupancy(n *NodeInfo, used int64) float64 {
	return float64(used) / float64(max(n.Capacity, 1))
}

func (s *Server) fits(n *NodeInfo, size int64) bool {
	return float64(n.Used+size) <= float64(n.Capacity)*s.cfg.FullThreshold
}

func sortByOccupancy(nodes []*NodeInfo, used func(*NodeInfo) int64) {
	sort.Slice(nodes, func(i, j int) bool {
		return float64(used(nodes[i]))/float64(max(nodes[i].Capacity, 1)) < float64(used(nodes[j]))/float64(max(nodes[j].Capacity, 1))
	})
}

func contains(list []string, item string) bool {
	for _, x := range list {
		if x == item {
			return true
		}
	}
	return false
}

func sameIDs(a, b []string) bool {
	x := append([]string{}, a...)
	y := append([]string{}, b...)
	sort.Strings(x)
	sort.Strings(y)
	return strings.Join(x, ",") == strings.Join(y, ",")
}
