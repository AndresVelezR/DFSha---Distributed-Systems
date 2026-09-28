package main

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

// Config reúne los parámetros operativos del ControlNode (Hito 1, "Parámetros
// configurables"). Se leen de variables de entorno al arrancar, así que se
// cambian sin recompilar. Los valores por defecto son los del Hito 1.
type Config struct {
	Addr              string        // CN_ADDR
	NodeID            string        // NODE_ID
	ClusterKey        string        // CLUSTER_KEY
	ReplicationFactor int           // REPLICATION_FACTOR
	BlockSizing       BlockSizing   // BLOCK_SIZE_MIN_MB, BLOCK_SIZE_MAX_MB, BLOCKS_PER_NODE
	HeartbeatInterval time.Duration // HEARTBEAT_INTERVAL_SECONDS
	HeartbeatMisses   int           // HEARTBEAT_MISSES
	WriteLeaseTTL     time.Duration // WRITE_LEASE_TTL_SECONDS
}

// BlockSizing son los parámetros de la fórmula de tamaño de bloque [N3].
type BlockSizing struct {
	Min           int64 // piso del tamaño de bloque, en bytes
	Max           int64 // techo del tamaño de bloque, en bytes
	BlocksPerNode int64 // k: bloques mínimos por nodo que debe producir un archivo
}

// HeartbeatTimeout es cuánto tiempo sin heartbeat hace falta para dar por
// muerto a un DataNode: heartbeat_interval x heartbeat_misses [N9].
func (c Config) HeartbeatTimeout() time.Duration {
	return c.HeartbeatInterval * time.Duration(c.HeartbeatMisses)
}

const megabyte = 1024 * 1024

func loadConfig() (Config, error) {
	var errs []error
	intVar := func(key string, def int) int {
		v, err := getenvInt(key, def)
		if err != nil {
			errs = append(errs, err)
		}
		return v
	}
	cfg := Config{
		Addr:              getenv("CN_ADDR", ":8000"),
		NodeID:            getenv("NODE_ID", "cn-01"),
		ClusterKey:        getenv("CLUSTER_KEY", "dev-cluster-key"),
		ReplicationFactor: intVar("REPLICATION_FACTOR", 2),
		BlockSizing: BlockSizing{
			Min:           int64(intVar("BLOCK_SIZE_MIN_MB", 8)) * megabyte,
			Max:           int64(intVar("BLOCK_SIZE_MAX_MB", 64)) * megabyte,
			BlocksPerNode: int64(intVar("BLOCKS_PER_NODE", 4)),
		},
		HeartbeatInterval: time.Duration(intVar("HEARTBEAT_INTERVAL_SECONDS", 3)) * time.Second,
		HeartbeatMisses:   intVar("HEARTBEAT_MISSES", 3),
		WriteLeaseTTL:     time.Duration(intVar("WRITE_LEASE_TTL_SECONDS", 60)) * time.Second,
	}
	if len(errs) > 0 {
		return cfg, errs[0]
	}
	return cfg, cfg.validate()
}

func (c Config) validate() error {
	switch {
	case c.ReplicationFactor < 1:
		return fmt.Errorf("REPLICATION_FACTOR debe ser al menos 1")
	case c.BlockSizing.Min < 1:
		return fmt.Errorf("BLOCK_SIZE_MIN_MB debe ser al menos 1")
	case c.BlockSizing.Max < c.BlockSizing.Min:
		return fmt.Errorf("BLOCK_SIZE_MAX_MB no puede ser menor que BLOCK_SIZE_MIN_MB")
	case c.BlockSizing.BlocksPerNode < 1:
		return fmt.Errorf("BLOCKS_PER_NODE debe ser al menos 1")
	case c.HeartbeatInterval < time.Second:
		return fmt.Errorf("HEARTBEAT_INTERVAL_SECONDS debe ser al menos 1")
	case c.HeartbeatMisses < 1:
		return fmt.Errorf("HEARTBEAT_MISSES debe ser al menos 1")
	case c.WriteLeaseTTL < time.Second:
		return fmt.Errorf("WRITE_LEASE_TTL_SECONDS debe ser al menos 1")
	}
	return nil
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func getenvInt(key string, def int) (int, error) {
	raw := os.Getenv(key)
	if raw == "" {
		return def, nil
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return def, fmt.Errorf("%s=%q no es un entero", key, raw)
	}
	return v, nil
}
