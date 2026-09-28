package main

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

// loadNode arma la configuración del DataNode desde variables de entorno.
// Los valores por defecto sirven para correrlo local junto a un ControlNode
// en localhost:8000.
func loadNode() (*Node, error) {
	port, err := getenvInt("PORT", 8001)
	if err != nil {
		return nil, err
	}
	heartbeatSeconds, err := getenvInt("HEARTBEAT_INTERVAL_SECONDS", 3)
	if err != nil {
		return nil, err
	}
	if heartbeatSeconds < 1 {
		return nil, fmt.Errorf("HEARTBEAT_INTERVAL_SECONDS debe ser al menos 1")
	}
	return &Node{
		ID:                getenv("NODE_ID", "dn-01"),
		Host:              getenv("ADVERTISE_HOST", "localhost"),
		Port:              port,
		ControlURL:        getenv("CONTROL_URL", "http://localhost:8000"),
		StorageDir:        getenv("STORAGE_DIR", "./data"),
		ClusterKey:        getenv("CLUSTER_KEY", "dev-cluster-key"),
		ClientToken:       getenv("CLIENT_TOKEN", "dfsha-dev-token"),
		HeartbeatInterval: time.Duration(heartbeatSeconds) * time.Second,
	}, nil
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
