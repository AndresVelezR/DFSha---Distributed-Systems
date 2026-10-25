package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"
)

func (n *Node) status() map[string]any {
	_, used := n.localInventory()
	return map[string]any{"node_id": n.ID, "url": n.URL, "capacity": n.Capacity, "used": used}
}

func (n *Node) register() error {
	return n.postControl("/cluster/register", n.status())
}

func (n *Node) blockReport() error {
	blocks, _ := n.localInventory()
	return n.postControl("/cluster/blockreport", map[string]any{"node_id": n.ID, "blocks": blocks})
}

// Registro, señales de vida y reportes se reintentan durante elecciones.
func (n *Node) controlLoop(ctx context.Context) {
	if err := n.register(); err != nil {
		log.Printf("registro inicial: %v", err)
	}
	if err := n.blockReport(); err != nil {
		log.Printf("reporte inicial: %v", err)
	}
	hb := time.NewTicker(interval("HEARTBEAT_MS", 3*time.Second))
	report := time.NewTicker(interval("REPORT_MS", 30*time.Second))
	defer hb.Stop()
	defer report.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-hb.C:
			if err := n.postControl("/cluster/heartbeat", n.status()); err != nil {
				log.Printf("heartbeat: %v", err)
				if err := n.register(); err != nil {
					log.Printf("registro: %v", err)
				}
			}
		case <-report.C:
			if err := n.blockReport(); err != nil {
				log.Printf("reporte: %v", err)
			}
		}
	}
}

// Contacta a todos los ControlNodes; basta que uno acepte. Los seguidores
// pueden responder 503 hasta que haya un líder con mayoría disponible.
func (n *Node) postControl(endpoint string, body any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	// Se envía a los tres ControlNodes en paralelo, con un plazo cada uno,
	// para que uno caído no retrase el heartbeat que le llega al líder.
	results := make(chan error, len(n.ControlURLs))
	for _, base := range n.ControlURLs {
		go func(base string) { results <- n.postOneControl(base, endpoint, b) }(base)
	}
	accepted := false
	var lastErr error
	for range n.ControlURLs {
		if err := <-results; err == nil {
			accepted = true
		} else {
			lastErr = err
		}
	}
	if accepted {
		return nil
	}
	if lastErr == nil {
		lastErr = errors.New("no hay ControlNodes configurados")
	}
	return lastErr
}

func (n *Node) postOneControl(base, endpoint string, body []byte) error {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Cluster-Key", n.Config.ClusterKey)
	resp, err := n.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 8192))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("%s %s: %s", base, endpoint, resp.Status)
	}
	return nil
}
