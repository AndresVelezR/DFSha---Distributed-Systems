package main

import (
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"dfsha/internal/common"

	"github.com/hashicorp/raft"
)

// retiredReplica es una copia que dejó de ser una ubicación activa (por
// rebalanceo o porque sobraba) y que se borra físicamente después de At.
type retiredReplica struct {
	Node, Block string
	At          time.Time
}

// Server es un ControlNode. Los metadatos (State) viven en la máquina de
// estados de Raft y son iguales en los tres ControlNodes. Lo demás es memoria
// local del líder que se reconstruye sola: los DataNodes vuelven a mandar
// heartbeat al líder nuevo y el inventario se vuelve a pedir.
type Server struct {
	cfg  Config
	fsm  *metadataFSM
	raft *raft.Raft

	// op serializa las peticiones: cada una lee el estado, decide y lo
	// publica por Raft sin que otra se le cruce en medio.
	op sync.Mutex

	nodes       map[string]*NodeInfo // DataNodes conocidos, por node_id
	retired     map[string]retiredReplica
	orphanSince map[string]time.Time
	users       map[string]string             // usuario -> hash bcrypt
	peerHTTP    map[raft.ServerAddress]string // dirección Raft -> URL de la API
	started     time.Time

	client      *http.Client // copias de bloques entre DataNodes
	quickClient *http.Client // consultas cortas: un nodo colgado no frena al líder
}

func newServer(cfg Config) *Server {
	quick := *common.HTTPClient(cfg.Security)
	quick.Timeout = 10 * time.Second
	return &Server{
		cfg:         cfg,
		fsm:         &metadataFSM{state: emptyState()},
		nodes:       map[string]*NodeInfo{},
		retired:     map[string]retiredReplica{},
		orphanSince: map[string]time.Time{},
		users:       map[string]string{},
		peerHTTP:    map[raft.ServerAddress]string{},
		started:     time.Now(),
		client:      common.HTTPClient(cfg.Security),
		quickClient: &quick,
	}
}

// state devuelve una copia del estado confirmado. Los handlers modifican la
// copia y la publican con save; el original solo cambia cuando Raft la aplica.
func (s *Server) state() *State {
	s.fsm.mu.RLock()
	defer s.fsm.mu.RUnlock()
	b, _ := json.Marshal(s.fsm.state)
	var v State
	_ = json.Unmarshal(b, &v)
	return &v
}

// save replica el estado nuevo por Raft. Retorna cuando una mayoría de
// ControlNodes lo guardó en disco y la máquina de estados lo aceptó.
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

	// Cliente: requieren token de sesión y que este nodo sea el líder.
	client := map[string]http.HandlerFunc{
		"GET /fs/ls":                s.fsLS,
		"POST /fs/mkdir":            s.fsMkdir,
		"DELETE /fs/rmdir":          s.fsRmdir,
		"DELETE /fs/rm":             s.fsRM,
		"POST /fs/mv":               s.fsMV,
		"GET /fs/stat":              s.fsStat,
		"POST /files/upload/plan":   s.uploadPlan,
		"POST /files/upload/renew":  s.uploadRenew,
		"POST /files/upload/commit": s.uploadCommit,
		"POST /files/upload/abort":  s.uploadAbort,
		"GET /files/download/plan":  s.downloadPlan,
		"POST /files/open":          s.openFile,
		"POST /files/lock":          s.lockFile,
		"POST /files/renew":         s.renewHandle,
		"POST /files/close":         s.closeFile,
		"GET /cluster/status":       s.status,
	}
	for route, fn := range client {
		mux.HandleFunc(route, s.withAuth(fn))
	}

	// DataNodes: requieren la clave del clúster.
	cluster := map[string]http.HandlerFunc{
		"POST /cluster/register":    s.clusterRegister,
		"POST /cluster/heartbeat":   s.clusterHeartbeat,
		"POST /cluster/blockreport": s.clusterBlockReport,
	}
	for route, fn := range cluster {
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

// leader comprueba que este nodo es el líder, que todavía tiene mayoría y que
// ya aplicó todo lo confirmado. Si no, responde 503 con la URL del líder para
// que el cliente lo vuelva a intentar allá.
func (s *Server) leader(w http.ResponseWriter) bool {
	if s.raft == nil || s.raft.State() != raft.Leader {
		addr, _ := s.raft.LeaderWithID()
		common.JSON(w, 503, map[string]any{"error": "este ControlNode no es el líder", "leader": s.peerHTTP[addr]})
		return false
	}
	if e := s.raft.VerifyLeader().Error(); e != nil {
		fail(w, 503, "sin mayoría de ControlNodes")
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
		fail(w, 503, "no se pudo confirmar la operación; reintentar")
		return false
	}
	return true
}
