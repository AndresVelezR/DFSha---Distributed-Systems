package main

import (
	"bytes"
	"encoding/json"
	"github.com/hashicorp/raft"
	"log"
	"net/http"
	"sort"
	"strings"
	"time"
)

func (s *Server) internal(method, url string, body any) (*http.Response, error) {
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
	return s.client.Do(req)
}
func (s *Server) inventory(n *NodeInfo) bool {
	resp, e := s.internal("GET", n.URL+"/inventory", nil)
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
		}
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
func (s *Server) copyBlock(src, dst *NodeInfo, b StoredBlock) bool {
	resp, e := s.internal("POST", src.URL+"/blocks/"+b.BlockID+"/replicate", map[string]any{"target": Target{Node: dst.NodeID, URL: dst.URL}})
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
	dst.Used += b.Size + 32
	return true
}
func sameIDs(a, b []string) bool {
	x := append([]string{}, a...)
	y := append([]string{}, b...)
	sort.Strings(x)
	sort.Strings(y)
	return strings.Join(x, ",") == strings.Join(y, ",")
}

// Retained copies consume disk space but must not trigger another relocation:
// balance decisions use active placement usage; admission still uses real disk.
func (s *Server) balanceUsed(n *NodeInfo) int64 {
	used := n.Used
	for _, retired := range s.retired {
		if retired.Node == n.NodeID {
			if size, exists := n.Blocks[retired.Block]; exists {
				used -= size + 32
			}
		}
	}
	return max(used, 0)
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
	changed := false
	now := time.Now()
	activeUpload := false
	for id, up := range v.Uploads {
		if now.After(up.ExpiresAt) {
			delete(v.Uploads, id)
			changed = true
		} else {
			activeUpload = true
		}
	}
	for id, h := range v.Handles {
		if now.After(h.ExpiresAt) {
			delete(v.Handles, id)
			changed = true
		}
	}
	nodes := s.aliveNodes()
	verified := []*NodeInfo{}
	for _, n := range nodes {
		if s.inventory(s.nodes[n.NodeID]) {
			verified = append(verified, s.nodes[n.NodeID])
		}
	}
	refs := map[string]StoredBlock{}
	eachBlock(v, func(b *StoredBlock) { refs[b.BlockID] = *b })
	desired, _ := copiesPerBlock(s.cfg.ReplicationFactor, len(s.nodes), len(verified))
	desired = min(desired, len(verified))
	moved := false
	for id, b := range refs {
		holders := []*NodeInfo{}
		allHolders := []*NodeInfo{}
		for _, n := range verified {
			if z, ok := n.Blocks[id]; ok && z == b.Size {
				allHolders = append(allHolders, n)
				if _, retired := s.retired[n.NodeID+":"+id]; !retired {
					holders = append(holders, n)
				}
			}
		}
		if len(holders) < desired {
			for _, n := range allHolders {
				if _, retired := s.retired[n.NodeID+":"+id]; retired {
					delete(s.retired, n.NodeID+":"+id)
					holders = append(holders, n)
					if len(holders) >= desired {
						break
					}
				}
			}
		}
		if len(holders) > desired && desired > 0 && !activeUpload {
			sort.Slice(holders, func(i, j int) bool {
				return float64(holders[i].Used)/float64(holders[i].Capacity) < float64(holders[j].Used)/float64(holders[j].Capacity)
			})
			for _, n := range holders[desired:] {
				s.retired[n.NodeID+":"+id] = retiredReplica{Node: n.NodeID, Block: id, At: now.Add(s.cfg.GCGrace)}
			}
			holders = holders[:desired]
			moved = true
		}
		if len(holders) == 0 {
			continue
		}
		if len(holders) < desired && !moved {
			sort.Slice(verified, func(i, j int) bool {
				return float64(verified[i].Used)/float64(verified[i].Capacity) < float64(verified[j].Used)/float64(verified[j].Capacity)
			})
			for _, n := range verified {
				if _, has := n.Blocks[id]; !has && float64(n.Used+b.Size+32) <= float64(n.Capacity)*s.cfg.FullThreshold {
					if s.copyBlock(holders[0], n, b) {
						holders = append(holders, n)
						moved = true
						log.Printf("repair block=%s destination=%s", id, n.NodeID)
					}
					break
				}
			}
		}
		ids := []string{}
		for _, n := range holders {
			ids = append(ids, n.NodeID)
		}
		if len(ids) > 0 {
			eachBlock(v, func(x *StoredBlock) {
				if x.BlockID == id && !sameIDs(x.StoredOn, ids) {
					x.StoredOn = append([]string{}, ids...)
					changed = true
				}
			})
		}
	}
	// At most one relocation per pass bounds background traffic. Publish the new
	// map through Raft before deleting the source, never the other way around.

	refs = map[string]StoredBlock{}
	eachBlock(v, func(b *StoredBlock) { refs[b.BlockID] = *b })
	if !moved && !activeUpload && len(verified) > 1 {
		sort.Slice(verified, func(i, j int) bool {
			return float64(s.balanceUsed(verified[i]))/float64(verified[i].Capacity) < float64(s.balanceUsed(verified[j]))/float64(verified[j].Capacity)
		})
		lo, hi := verified[0], verified[len(verified)-1]
		gap := float64(s.balanceUsed(hi))/float64(hi.Capacity) - float64(s.balanceUsed(lo))/float64(lo.Capacity)
		if gap > s.cfg.RebalanceThreshold {
			for id, b := range refs {
				_, from := hi.Blocks[id]
				// A physically retained old copy is not an active placement.
				activeSource := false
				for _, nodeID := range b.StoredOn {
					if nodeID == hi.NodeID {
						activeSource = true
					}
				}
				_, to := lo.Blocks[id]
				nextGap := float64(s.balanceUsed(hi)-b.Size)/float64(hi.Capacity) - float64(s.balanceUsed(lo)+b.Size)/float64(lo.Capacity)
				if from && activeSource && !to && len(b.StoredOn) >= desired && float64(lo.Used+b.Size+32) <= float64(lo.Capacity)*s.cfg.FullThreshold && nextGap >= 0 && nextGap < gap && s.copyBlock(hi, lo, b) {
					eachBlock(v, func(x *StoredBlock) {
						if x.BlockID == id {
							ids := []string{}
							for _, n := range x.StoredOn {
								if n != hi.NodeID {
									ids = append(ids, n)
								}
							}
							ids = append(ids, lo.NodeID)
							x.StoredOn = ids
						}
					})
					changed = true
					s.retired[hi.NodeID+":"+id] = retiredReplica{Node: hi.NodeID, Block: id, At: now.Add(s.cfg.GCGrace)}
					log.Printf("rebalance block=%s source=%s target=%s", id, hi.NodeID, lo.NodeID)
					break
				}
			}
		}
	}
	if changed {
		if e := s.save(v); e != nil {
			log.Printf("maintenance metadata: %v", e)
			return
		}
	}
	// Retain old locations until all previously issued capabilities expire.
	if !activeUpload {
		for k, d := range s.retired {
			if now.Before(d.At) {
				continue
			}
			n := s.nodes[d.Node]
			if n == nil || !s.isAlive(n) {
				continue
			}
			b, used := refs[d.Block]
			if used {
				count := 0
				for _, other := range verified {
					if other.NodeID != n.NodeID {
						if size, ok := other.Blocks[d.Block]; ok && size == b.Size {
							count++
						}
					}
				}
				if count < max(desired, 1) {
					continue
				}
			}
			if s.raft.VerifyLeader().Error() != nil {
				return
			}
			resp, e := s.internal("DELETE", n.URL+"/blocks/"+d.Block, nil)
			if e == nil {
				resp.Body.Close()
				if resp.StatusCode < 300 {
					delete(n.Blocks, d.Block)
					delete(s.retired, k)
				}
			}
		}
	}

	// Delay deletion beyond every issued read/write capability. During a live
	// upload its hashes are unknown, so GC conservatively pauses cluster-wide.
	if activeUpload {
		return
	}
	for _, n := range verified {
		for id := range n.Blocks {
			orphanKey := n.NodeID + ":" + id
			if _, used := refs[id]; used {
				delete(s.orphanSince, orphanKey)
				continue
			}
			first, seen := s.orphanSince[orphanKey]
			if !seen {
				s.orphanSince[orphanKey] = now
				continue
			}
			if now.Sub(first) >= s.cfg.GCGrace && time.Since(s.started) >= s.cfg.GCGrace && s.raft.VerifyLeader().Error() == nil {
				resp, e := s.internal("DELETE", n.URL+"/blocks/"+id, nil)
				if e == nil {
					resp.Body.Close()
					delete(n.Blocks, id)
					delete(s.orphanSince, orphanKey)
				}
			}
		}
	}
}
