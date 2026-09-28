package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"
)

// Comunicación DataNode -> ControlNode: registro y señales de vida.

func (n *Node) register() error {
	capacity, used := usage(n.StorageDir)
	body := map[string]any{"node_id": n.ID, "host": n.Host, "port": n.Port, "capacity": capacity, "used": used}
	return n.postControl("/cluster/register", body)
}

// heartbeatLoop avisa periódicamente que el nodo sigue vivo y cuántos bytes
// de bloques guarda. Si el ControlNode no lo reconoce (por ejemplo, porque se reinició),
// vuelve a registrarse.
func (n *Node) heartbeatLoop() {
	ticker := time.NewTicker(n.HeartbeatInterval)
	defer ticker.Stop()
	for range ticker.C {
		capacity, used := usage(n.StorageDir)
		body := map[string]any{"node_id": n.ID, "capacity": capacity, "used": used}
		if err := n.postControl("/cluster/heartbeat", body); err != nil {
			log.Printf("heartbeat: %v; reintentando registro", err)
			_ = n.register()
		}
	}
}

func (n *Node) postControl(endpoint string, body any) error {
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, n.ControlURL+endpoint, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Cluster-Key", n.ClusterKey)
	c := &http.Client{Timeout: 4 * time.Second}
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		x, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("%s: %s", resp.Status, string(x))
	}
	return nil
}
