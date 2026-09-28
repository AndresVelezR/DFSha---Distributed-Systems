package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// Comunicación DataNode -> DataNode: envío de un bloque a otro nodo.

// forwardFile envía el bloque guardado en file al DataNode con URL base.
// Se autentica con la clave del clúster, no con el token del cliente.
func (n *Node) forwardFile(base, id, file string) error {
	f, err := os.Open(file)
	if err != nil {
		return err
	}
	defer f.Close()
	req, err := http.NewRequest(http.MethodPut, strings.TrimRight(base, "/")+"/blocks/"+id, f)
	if err != nil {
		return err
	}
	req.Header.Set("X-Cluster-Key", n.ClusterKey)
	req.Header.Set("Content-Type", "application/octet-stream")
	c := &http.Client{Timeout: 2 * time.Minute}
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("%s %s", resp.Status, string(b))
	}
	return nil
}
