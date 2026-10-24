package main

import (
	"dfsha/internal/common"
	"fmt"
	"os"
	"strconv"
	"time"
)

const megabyte = 1024 * 1024

type BlockSizing struct{ Min, Max, BlocksPerNode int64 }
type Config struct {
	Addr, NodeID, DataDir, RaftAddr, RaftAdvertise, Peers, UsersFile string
	Security                                                         common.Config
	ReplicationFactor                                                int
	BlockSizing                                                      BlockSizing
	HeartbeatInterval                                                time.Duration
	HeartbeatMisses                                                  int
	WriteLeaseTTL, MaintenanceInterval, GCGrace                      time.Duration
	FullThreshold, RebalanceThreshold                                float64
}

func (c Config) HeartbeatTimeout() time.Duration {
	return c.HeartbeatInterval * time.Duration(c.HeartbeatMisses)
}
func getenv(k, d string) string { return common.Env(k, d) }
func envInt(k string, d int) int {
	v, e := strconv.Atoi(getenv(k, strconv.Itoa(d)))
	if e != nil || v < 1 {
		panic(fmt.Sprintf("%s entero positivo obligatorio", k))
	}
	return v
}
func loadConfig() (Config, error) {
	sec, e := common.LoadConfig()
	if e != nil {
		return Config{}, e
	}
	c := Config{Addr: getenv("CN_ADDR", ":8000"), NodeID: getenv("NODE_ID", "cn-01"), DataDir: getenv("METADATA_DIR", "data/control"), RaftAddr: getenv("RAFT_ADDR", ":9000"), RaftAdvertise: getenv("RAFT_ADVERTISE", "localhost:9000"), Peers: getenv("CN_PEERS", "cn-01@localhost:9000@https://localhost:8000"), UsersFile: getenv("USERS_FILE", ".secrets/users.json"), Security: sec, ReplicationFactor: envInt("REPLICATION_FACTOR", 2), BlockSizing: BlockSizing{int64(envInt("BLOCK_SIZE_MIN_MB", 8)) * megabyte, int64(envInt("BLOCK_SIZE_MAX_MB", 64)) * megabyte, int64(envInt("BLOCKS_PER_NODE", 4))}, HeartbeatInterval: time.Duration(envInt("HEARTBEAT_MS", 3000)) * time.Millisecond, HeartbeatMisses: envInt("HEARTBEAT_MISSES", 3), WriteLeaseTTL: time.Duration(envInt("WRITE_LEASE_SECONDS", 60)) * time.Second, MaintenanceInterval: time.Duration(envInt("MAINTENANCE_MS", 5000)) * time.Millisecond, GCGrace: time.Duration(envInt("GC_GRACE_SECONDS", 120)) * time.Second, FullThreshold: float64(envInt("FULL_THRESHOLD_PERCENT", 90)) / 100, RebalanceThreshold: float64(envInt("REBALANCE_THRESHOLD_PERCENT", 10)) / 100}
	if c.BlockSizing.Min < 8*megabyte || c.BlockSizing.Max > 64*megabyte || c.BlockSizing.Max < c.BlockSizing.Min {
		return c, fmt.Errorf("bloques deben estar entre8 y64 MiB")
	}
	if c.FullThreshold > 1 || c.RebalanceThreshold > 1 {
		return c, fmt.Errorf("umbrales <=100")
	}
	if c.GCGrace < time.Minute*2 {
		return c, fmt.Errorf("GC_GRACE_SECONDS mínimo120 protege capacidades pendientes")
	}
	if _, e = os.Stat(c.UsersFile); e != nil {
		return c, e
	}
	return c, nil
}
