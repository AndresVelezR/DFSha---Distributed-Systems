package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"dfsha/internal/common"
)

func (n *Node) targets(r *http.Request, id string, size int64) ([]forwardTarget, error) {
	var targets []forwardTarget
	header := strings.TrimSpace(r.Header.Get("X-Forward-To"))
	if header == "" {
		return targets, nil
	}
	if len(header) > 32768 || json.Unmarshal([]byte(header), &targets) != nil || len(targets) > 16 {
		return nil, errors.New("X-Forward-To debe ser una lista JSON de hasta 16 nodos")
	}
	seen := map[string]bool{n.ID: true}
	for _, target := range targets {
		if target.Node == "" || seen[target.Node] || n.validURL(target.URL) != nil {
			return nil, errors.New("destino de réplica no válido o duplicado")
		}
		seen[target.Node] = true
		c, err := common.Verify(target.Token, n.Config.SigningKey)
		if err != nil || c.Kind != "block-write" || c.Node != target.Node || (c.Block != id && c.Block != "*") || (c.MaxSize > 0 && size > c.MaxSize) {
			return nil, errors.New("capacidad de réplica no válida")
		}
	}
	return targets, nil
}

// Transfiere el bloque binario descifrado por HTTPS; cada destino usa su
// propio nonce AES-GCM. Sólo las reparaciones internas usan X-Cluster-Key.
func (n *Node) forwardBlock(ctx context.Context, target forwardTarget, id string, plain []byte, internal bool) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, strings.TrimRight(target.URL, "/")+"/blocks/"+id, bytes.NewReader(plain))
	if err != nil {
		return err
	}
	if internal {
		req.Header.Set("X-Cluster-Key", n.Config.ClusterKey)
	} else {
		req.Header.Set("Authorization", "Bearer "+target.Token)
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, err := n.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var result struct {
		BlockID string   `json:"block_id"`
		Size    int64    `json:"size"`
		Stored  []string `json:"stored_on"`
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("destino devolvió %s", resp.Status)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 16384)).Decode(&result); err != nil {
		return err
	}
	if result.BlockID != id || result.Size != int64(len(plain)) {
		return errors.New("confirmación de réplica no válida")
	}
	for _, node := range result.Stored {
		if node == target.Node {
			return nil
		}
	}
	return errors.New("destino no confirmó almacenamiento")
}
