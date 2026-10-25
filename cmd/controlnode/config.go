package main

import (
	"fmt"
	"os"
	"strconv"
	"time"

	"dfsha/internal/common"
)

const megabyte = 1024 * 1024

// maxBlocksPerFile limita el tamaño de un manifiesto. Con bloques de 64 MiB
// equivale a archivos de más de 6 TiB.
const maxBlocksPerFile = 100000

type BlockSizing struct{ Min, Max, BlocksPerNode int64 }

// Config son los parámetros del ControlNode. Todos se leen de variables de
// entorno al arrancar, así que se cambian sin recompilar.
type Config struct {
	Addr          string // API HTTPS, por ejemplo :8000
	NodeID        string // cn-01, cn-02 o cn-03
	DataDir       string // registro de Raft y snapshots
	RaftAddr      string // dónde escucha Raft, por ejemplo :9000
	RaftAdvertise string // cómo lo alcanzan los otros ControlNodes
	Peers         string // los tres ControlNodes: id@host:raft@https://host:api
	UsersFile     string // usuario -> hash bcrypt
	Security      common.Config

	ReplicationFactor   int
	BlockSizing         BlockSizing
	HeartbeatInterval   time.Duration
	HeartbeatMisses     int
	WriteLeaseTTL       time.Duration
	MaintenanceInterval time.Duration
	GCGrace             time.Duration
	FullThreshold       float64 // fracción de capacidad a la que un nodo deja de recibir bloques
	RebalanceThreshold  float64 // diferencia de ocupación que dispara el rebalanceo
}

// HeartbeatTimeout es cuánto tiempo sin heartbeat basta para dar a un
// DataNode por caído: 3 s x 3 = 9 s con los valores por defecto.
func (c Config) HeartbeatTimeout() time.Duration {
	return c.HeartbeatInterval * time.Duration(c.HeartbeatMisses)
}

func getenv(k, d string) string { return common.Env(k, d) }

func envInt(k string, d int) (int, error) {
	v, e := strconv.Atoi(getenv(k, strconv.Itoa(d)))
	if e != nil || v < 1 {
		return 0, fmt.Errorf("%s debe ser un entero positivo", k)
	}
	return v, nil
}

func loadConfig() (Config, error) {
	sec, e := common.LoadConfig()
	if e != nil {
		return Config{}, e
	}
	ints := map[string]int{
		"REPLICATION_FACTOR": 2, "BLOCK_SIZE_MIN_MB": 8, "BLOCK_SIZE_MAX_MB": 64, "BLOCKS_PER_NODE": 4,
		"HEARTBEAT_MS": 3000, "HEARTBEAT_MISSES": 3, "WRITE_LEASE_SECONDS": 60, "MAINTENANCE_MS": 5000,
		"GC_GRACE_SECONDS": 120, "FULL_THRESHOLD_PERCENT": 90, "REBALANCE_THRESHOLD_PERCENT": 10,
	}
	for k, d := range ints {
		if ints[k], e = envInt(k, d); e != nil {
			return Config{}, e
		}
	}
	c := Config{
		Addr:          getenv("CN_ADDR", ":8000"),
		NodeID:        getenv("NODE_ID", "cn-01"),
		DataDir:       getenv("METADATA_DIR", "data/control"),
		RaftAddr:      getenv("RAFT_ADDR", ":9000"),
		RaftAdvertise: getenv("RAFT_ADVERTISE", "localhost:9000"),
		Peers:         getenv("CN_PEERS", "cn-01@localhost:9000@https://localhost:8000"),
		UsersFile:     getenv("USERS_FILE", ".secrets/users.json"),
		Security:      sec,

		ReplicationFactor: ints["REPLICATION_FACTOR"],
		BlockSizing: BlockSizing{
			Min:           int64(ints["BLOCK_SIZE_MIN_MB"]) * megabyte,
			Max:           int64(ints["BLOCK_SIZE_MAX_MB"]) * megabyte,
			BlocksPerNode: int64(ints["BLOCKS_PER_NODE"]),
		},
		HeartbeatInterval:   time.Duration(ints["HEARTBEAT_MS"]) * time.Millisecond,
		HeartbeatMisses:     ints["HEARTBEAT_MISSES"],
		WriteLeaseTTL:       time.Duration(ints["WRITE_LEASE_SECONDS"]) * time.Second,
		MaintenanceInterval: time.Duration(ints["MAINTENANCE_MS"]) * time.Millisecond,
		GCGrace:             time.Duration(ints["GC_GRACE_SECONDS"]) * time.Second,
		FullThreshold:       float64(ints["FULL_THRESHOLD_PERCENT"]) / 100,
		RebalanceThreshold:  float64(ints["REBALANCE_THRESHOLD_PERCENT"]) / 100,
	}
	if c.BlockSizing.Min < 8*megabyte || c.BlockSizing.Max > 64*megabyte || c.BlockSizing.Max < c.BlockSizing.Min {
		return c, fmt.Errorf("el tamaño de bloque debe estar entre 8 y 64 MiB")
	}
	if c.FullThreshold > 1 || c.RebalanceThreshold > 1 {
		return c, fmt.Errorf("los umbrales de ocupación no pueden pasar de 100")
	}
	// Los permisos de lectura y escritura de bloques duran hasta 60 s; la
	// gracia de borrado tiene que ser mayor para no borrar algo en uso.
	if c.GCGrace < 2*time.Minute {
		return c, fmt.Errorf("GC_GRACE_SECONDS debe ser al menos 120")
	}
	if _, e = os.Stat(c.UsersFile); e != nil {
		return c, e
	}
	return c, nil
}
