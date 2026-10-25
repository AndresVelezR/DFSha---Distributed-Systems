package main

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"dfsha/internal/common"

	"github.com/hashicorp/raft"
	bolt "github.com/hashicorp/raft-boltdb/v2"
)

// metadataFSM es la máquina de estados que replica Raft. Ningún handler
// cambia el estado directamente: propone un estado nuevo con save y solo se
// aplica aquí cuando una mayoría de ControlNodes lo guardó en su registro.
type metadataFSM struct {
	mu    sync.RWMutex
	state *State
}

// Apply recibe cada entrada confirmada del registro, en el mismo orden en los
// tres ControlNodes. Cada entrada es el estado completo con un número de
// revisión: solo se acepta la revisión siguiente a la actual, así que un
// estado viejo nunca puede pisar uno más nuevo.
func (f *metadataFSM) Apply(l *raft.Log) any {
	var s State
	if e := json.Unmarshal(l.Data, &s); e != nil {
		return e
	}
	if s.Files == nil || s.Dirs == nil || s.Uploads == nil || s.Handles == nil || s.Completed == nil {
		return fmt.Errorf("estado inválido")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if s.Revision != f.state.Revision+1 {
		return fmt.Errorf("revisión obsoleta: recibido %d, esperado %d", s.Revision, f.state.Revision+1)
	}
	f.state = &s
	return nil
}

// Snapshot y Restore permiten a Raft compactar su registro: guarda una foto
// del estado y descarta las entradas anteriores.
type snapshot struct{ data []byte }

func (f *metadataFSM) Snapshot() (raft.FSMSnapshot, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	b, e := json.Marshal(f.state)
	return &snapshot{b}, e
}
func (s *snapshot) Persist(sink raft.SnapshotSink) error {
	if _, e := sink.Write(s.data); e != nil {
		_ = sink.Cancel()
		return e
	}
	return sink.Close()
}
func (s *snapshot) Release() {}
func (f *metadataFSM) Restore(r io.ReadCloser) error {
	defer r.Close()
	var v State
	if e := json.NewDecoder(r).Decode(&v); e != nil {
		return e
	}
	f.mu.Lock()
	f.state = &v
	f.mu.Unlock()
	return nil
}

// Raft viaja por TCP en RAFT_ADDR, con TLS mutuo: cada ControlNode presenta
// el certificado del clúster y exige el del otro.
type advertisedAddress string

func (a advertisedAddress) Network() string { return "tcp" }
func (a advertisedAddress) String() string  { return string(a) }

type streamLayer struct {
	net.Listener
	config    *tls.Config
	advertise net.Addr
}

func (s *streamLayer) Dial(addr raft.ServerAddress, timeout time.Duration) (net.Conn, error) {
	if s.config == nil {
		return net.DialTimeout("tcp", string(addr), timeout)
	}
	return tls.DialWithDialer(&net.Dialer{Timeout: timeout}, "tcp", string(addr), s.config.Clone())
}
func (s *streamLayer) Addr() net.Addr { return s.advertise }

// startRaft abre el registro persistente (BoltDB) y los snapshots en
// METADATA_DIR y se une a los otros dos ControlNodes de CN_PEERS. La primera
// vez que arranca un clúster vacío, lo inicializa con esos tres votantes.
func (s *Server) startRaft() error {
	if e := os.MkdirAll(s.cfg.DataDir, 0700); e != nil {
		return e
	}
	logs, e := bolt.NewBoltStore(filepath.Join(s.cfg.DataDir, "raft.db"))
	if e != nil {
		return e
	}
	snaps, e := raft.NewFileSnapshotStore(s.cfg.DataDir, 3, os.Stderr)
	if e != nil {
		return e
	}
	ln, e := net.Listen("tcp", s.cfg.RaftAddr)
	if e != nil {
		return e
	}
	_, _, e = net.SplitHostPort(s.cfg.RaftAdvertise)
	if e != nil {
		return e
	}
	var clientTLS *tls.Config
	if !s.cfg.Security.Insecure {
		serverTLS, err := common.TLSConfig(s.cfg.Security, true, true)
		if err != nil {
			return err
		}
		ln = tls.NewListener(ln, serverTLS)
		clientTLS, err = common.TLSConfig(s.cfg.Security, false, true)
		if err != nil {
			return err
		}
	}
	trans := raft.NewNetworkTransport(&streamLayer{ln, clientTLS, advertisedAddress(s.cfg.RaftAdvertise)}, 3, 5*time.Second, os.Stderr)
	cfg := raft.DefaultConfig()
	cfg.LocalID = raft.ServerID(s.cfg.NodeID)
	cfg.HeartbeatTimeout = 2 * time.Second
	cfg.ElectionTimeout = 2 * time.Second
	cfg.LeaderLeaseTimeout = time.Second
	cfg.CommitTimeout = 100 * time.Millisecond
	cfg.SnapshotThreshold = 64
	cfg.SnapshotInterval = 20 * time.Second
	existing, e := raft.HasExistingState(logs, logs, snaps)
	if e != nil {
		return e
	}
	s.raft, e = raft.NewRaft(cfg, s.fsm, logs, logs, snaps, trans)
	if e != nil {
		return e
	}
	configuration := raft.Configuration{}
	own := false
	for _, entry := range strings.Split(s.cfg.Peers, ",") {
		p := strings.Split(entry, "@")
		if len(p) != 3 {
			return fmt.Errorf("CN_PEERS formato id@host:raftport@https://host:apiport")
		}
		if p[0] == s.cfg.NodeID {
			own = true
			if p[1] != s.cfg.RaftAdvertise {
				return fmt.Errorf("RAFT_ADVERTISE debe coincidir con CN_PEERS")
			}
		}
		configuration.Servers = append(configuration.Servers, raft.Server{ID: raft.ServerID(p[0]), Address: raft.ServerAddress(p[1]), Suffrage: raft.Voter})
		s.peerHTTP[raft.ServerAddress(p[1])] = p[2]
	}
	if !own {
		return fmt.Errorf("NODE_ID ausente en CN_PEERS")
	}
	if len(configuration.Servers) != 3 {
		return fmt.Errorf("se requieren 3 ControlNodes en CN_PEERS")
	}
	if !existing {
		if e = s.raft.BootstrapCluster(configuration).Error(); e != nil {
			return e
		}
	}
	return nil
}
