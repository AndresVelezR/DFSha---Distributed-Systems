package main

import (
	"net/http"
	"sync"
	"time"
)

// Server guarda todo el estado del ControlNode. En este hito vive en memoria;
// la persistencia y la réplica entre ControlNodes llegan en el Hito 3.
type Server struct {
	mu      sync.RWMutex
	nodes   map[string]*NodeInfo      // DataNodes registrados, por node_id
	uploads map[string]*UploadSession // subidas en curso, por upload_id
	files   map[string]*FileMeta      // archivos publicados, por ruta
	dirs    map[string]bool           // directorios existentes, por ruta
}

func newServer() *Server {
	return &Server{
		nodes:   map[string]*NodeInfo{},
		uploads: map[string]*UploadSession{},
		files:   map[string]*FileMeta{},
		dirs:    map[string]bool{"/": true},
	}
}

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", s.health)
	mux.HandleFunc("/auth/login", s.login)

	mux.HandleFunc("/fs/ls", s.withAuth(s.fsLS))
	mux.HandleFunc("/fs/mkdir", s.withAuth(s.fsMkdir))
	mux.HandleFunc("/fs/rmdir", s.withAuth(s.fsRmdir))
	mux.HandleFunc("/fs/rm", s.withAuth(s.fsRM))
	mux.HandleFunc("/fs/mv", s.withAuth(s.fsMV))
	mux.HandleFunc("/fs/stat", s.withAuth(s.fsStat))

	mux.HandleFunc("/files/upload/plan", s.withAuth(s.uploadPlan))
	mux.HandleFunc("/files/upload/commit", s.withAuth(s.uploadCommit))
	mux.HandleFunc("/files/upload/abort", s.withAuth(s.uploadAbort))
	mux.HandleFunc("/files/download/plan", s.withAuth(s.downloadPlan))

	mux.HandleFunc("/cluster/register", s.withClusterKey(s.clusterRegister))
	mux.HandleFunc("/cluster/heartbeat", s.withClusterKey(s.clusterHeartbeat))
	mux.HandleFunc("/cluster/blockreport", s.withClusterKey(s.clusterBlockReport))
	mux.HandleFunc("/cluster/replicate", s.notImplemented)
	mux.HandleFunc("/cluster/election", s.notImplemented)

	return logging(mux)
}

// aliveNodes devuelve una copia de los DataNodes con heartbeat reciente.
// Debe llamarse con s.mu tomado.
func (s *Server) aliveNodes() []*NodeInfo {
	nodes := []*NodeInfo{}
	for _, n := range s.nodes {
		if isAlive(n) {
			copyN := *n
			nodes = append(nodes, &copyN)
		}
	}
	return nodes
}

func isAlive(n *NodeInfo) bool {
	return time.Since(n.LastSeen) <= heartbeatTimeout
}
