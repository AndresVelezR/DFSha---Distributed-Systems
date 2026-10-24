package main

import (
	"dfsha/internal/common"
	"encoding/json"
	"github.com/hashicorp/raft"
	"net/http"
	"sync"
	"time"
)

type retiredReplica struct {
	Node, Block string
	At          time.Time
}
type Server struct {
	retired     map[string]retiredReplica
	cfg         Config
	op          sync.Mutex
	fsm         *metadataFSM
	raft        *raft.Raft
	nodes       map[string]*NodeInfo
	client      *http.Client
	users       map[string]string
	peerHTTP    map[raft.ServerAddress]string
	orphanSince map[string]time.Time
	started     time.Time
}

func newServer(cfg Config) *Server {
	return &Server{retired: map[string]retiredReplica{}, cfg: cfg, fsm: &metadataFSM{state: emptyState()}, nodes: map[string]*NodeInfo{}, client: common.HTTPClient(cfg.Security), users: map[string]string{}, peerHTTP: map[raft.ServerAddress]string{}, orphanSince: map[string]time.Time{}, started: time.Now()}
}
func (s *Server) state() *State {
	s.fsm.mu.RLock()
	defer s.fsm.mu.RUnlock()
	b, _ := json.Marshal(s.fsm.state)
	var v State
	_ = json.Unmarshal(b, &v)
	return &v
}
func (s *Server) save(v *State) error {
	v.Revision++
	b, e := json.Marshal(v)
	if e != nil {
		return e
	}
	f := s.raft.Apply(b, 5*time.Second)
	if e = f.Error(); e != nil {
		return e
	}
	if e, ok := f.Response().(error); ok {
		return e
	}
	return nil
}
func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", s.health)
	mux.HandleFunc("POST /auth/login", s.login)
	for route, fn := range map[string]http.HandlerFunc{"GET /fs/ls": s.fsLS, "POST /fs/mkdir": s.fsMkdir, "DELETE /fs/rmdir": s.fsRmdir, "DELETE /fs/rm": s.fsRM, "POST /fs/mv": s.fsMV, "GET /fs/stat": s.fsStat, "POST /files/upload/plan": s.uploadPlan, "POST /files/upload/renew": s.uploadRenew, "POST /files/upload/commit": s.uploadCommit, "POST /files/upload/abort": s.uploadAbort, "GET /files/download/plan": s.downloadPlan, "POST /files/open": s.openFile, "POST /files/lock": s.lockFile, "POST /files/renew": s.renewHandle, "POST /files/close": s.closeFile, "GET /cluster/status": s.status} {
		mux.HandleFunc(route, s.withAuth(fn))
	}
	for route, fn := range map[string]http.HandlerFunc{"POST /cluster/register": s.clusterRegister, "POST /cluster/heartbeat": s.clusterHeartbeat, "POST /cluster/blockreport": s.clusterBlockReport, "POST /cluster/replicate": s.raftProtocol, "POST /cluster/election": s.raftProtocol} {
		mux.HandleFunc(route, s.withClusterKey(fn))
	}
	return mux
}
func (s *Server) aliveNodes() []*NodeInfo {
	v := []*NodeInfo{}
	for _, n := range s.nodes {
		if s.isAlive(n) {
			copyN := *n
			v = append(v, &copyN)
		}
	}
	return v
}
func (s *Server) isAlive(n *NodeInfo) bool { return time.Since(n.LastSeen) <= s.cfg.HeartbeatTimeout() }
func (s *Server) leader(w http.ResponseWriter) bool {
	if s.raft == nil || s.raft.State() != raft.Leader {
		addr, _ := s.raft.LeaderWithID()
		common.JSON(w, 503, map[string]any{"error": "líder no disponible en este nodo", "leader": s.peerHTTP[addr]})
		return false
	}
	if e := s.raft.VerifyLeader().Error(); e != nil {
		fail(w, 503, "sin mayoría")
		return false
	}
	if e := s.raft.Barrier(3 * time.Second).Error(); e != nil {
		fail(w, 503, "metadatos no disponibles")
		return false
	}
	return true
}
func (s *Server) persist(w http.ResponseWriter, v *State) bool {
	if e := s.save(v); e != nil {
		fail(w, 503, "resultado de operación incierto; reintentar con mismo identificador")
		return false
	}
	return true
}
