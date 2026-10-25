package main

import (
	"bytes"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"dfsha/internal/common"
)

func fail(w http.ResponseWriter, status int, message string) {
	common.JSON(w, status, map[string]any{"error": message})
}

func (n *Node) health(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		fail(w, http.StatusMethodNotAllowed, "método no permitido")
		return
	}
	common.JSON(w, http.StatusOK, map[string]any{"status": "ok", "node_id": n.ID})
}

func (n *Node) clusterAuthorized(r *http.Request) bool {
	return n.Config.ClusterKey != "" && subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Cluster-Key")), []byte(n.Config.ClusterKey)) == 1
}

func (n *Node) authorize(r *http.Request, kind, id string) (common.Claims, bool) {
	if n.clusterAuthorized(r) {
		return common.Claims{Kind: kind, Block: id, Node: n.ID, MaxSize: maxBlockSize}, true
	}
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") {
		return common.Claims{}, false
	}
	c, err := common.Verify(strings.TrimPrefix(auth, "Bearer "), n.Config.SigningKey)
	validBlock := c.Block == id || (kind == "block-write" && c.Block == "*")
	return c, err == nil && c.Kind == kind && c.Node == n.ID && validBlock
}

func (n *Node) blocks(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/blocks/"), "/")
	if len(parts) == 0 || !validID(parts[0]) || len(parts) > 2 || (len(parts) == 2 && parts[1] != "replicate") {
		fail(w, http.StatusBadRequest, "block id debe ser SHA-256 hexadecimal en minúsculas")
		return
	}
	id := parts[0]
	if len(parts) == 2 {
		n.replicate(w, r, id)
		return
	}
	switch r.Method {
	case http.MethodPut:
		claims, ok := n.authorize(r, "block-write", id)
		if !ok {
			fail(w, http.StatusUnauthorized, "capacidad de escritura no válida")
			return
		}
		n.putBlock(w, r, id, claims)
	case http.MethodGet, http.MethodHead:
		if _, ok := n.authorize(r, "block-read", id); !ok {
			fail(w, http.StatusUnauthorized, "capacidad de lectura no válida")
			return
		}
		n.getBlock(w, r, id)
	case http.MethodDelete:
		if !n.clusterAuthorized(r) {
			fail(w, http.StatusUnauthorized, "requiere autenticación interna")
			return
		}
		n.deleteBlock(w, r, id)
	default:
		fail(w, http.StatusMethodNotAllowed, "método no permitido")
	}
}

// Cada bloque AES-GCM se autentica completo. La memoria está acotada por el
// máximo de 64 MiB por solicitud; nunca se carga un archivo completo.
func (n *Node) putBlock(w http.ResponseWriter, r *http.Request, id string, claims common.Claims) {
	limit := maxBlockSize
	if claims.MaxSize > 0 && claims.MaxSize < limit {
		limit = claims.MaxSize
	}
	if r.ContentLength > limit {
		fail(w, http.StatusRequestEntityTooLarge, "bloque excede el tamaño autorizado")
		return
	}
	plain, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
	if err != nil {
		fail(w, http.StatusBadRequest, "no se pudo leer bloque")
		return
	}
	if int64(len(plain)) > limit {
		fail(w, http.StatusRequestEntityTooLarge, "bloque excede el tamaño autorizado")
		return
	}
	if hashID(plain) != id {
		fail(w, http.StatusConflict, "SHA-256 no coincide")
		return
	}
	targets, err := n.targets(r, id, int64(len(plain)))
	if err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	created, err := n.store(id, plain)
	if err != nil {
		if errors.Is(err, errCapacity) {
			fail(w, http.StatusInsufficientStorage, err.Error())
		} else {
			log.Printf("almacenar %s: %v", id, err)
			fail(w, http.StatusInternalServerError, "falló almacenamiento de bloque")
		}
		return
	}
	if claims.User != "" {
		if err := n.recordOwner(id, claims.User); err != nil {
			fail(w, 500, "falló registro de autoría del bloque")
			return
		}
	}
	stored := []string{n.ID}
	// Un reintento debe propagar incluso si el primario ya tenía el bloque.
	for _, target := range targets {
		if err := n.forwardBlock(r.Context(), target, id, plain, false); err != nil {
			log.Printf("réplica %s -> %s: %v", id, target.Node, err)
			continue
		}
		stored = append(stored, target.Node)
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	common.JSON(w, status, map[string]any{"block_id": id, "size": len(plain), "stored_on": stored})
}

func (n *Node) getBlock(w http.ResponseWriter, r *http.Request, id string) {
	if user := r.Header.Get("X-Block-Owner"); user != "" {
		if !n.clusterAuthorized(r) {
			fail(w, 401, "requiere autenticación interna")
			return
		}
		if !n.hasOwner(id, user) {
			fail(w, 404, "esta cuenta no ha presentado el contenido del bloque")
			return
		}
	}
	plain, err := n.readBlock(id)
	if err != nil {
		if os.IsNotExist(err) {
			fail(w, http.StatusNotFound, "bloque no existe")
		} else {
			log.Printf("integridad %s: %v", id, err)
			n.forgetVerified(id)
			fail(w, http.StatusInternalServerError, "bloque corrupto o ilegible")
		}
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("X-Block-SHA256", id)
	w.Header().Set("ETag", `"`+id+`"`)
	http.ServeContent(w, r, id, time.Time{}, bytes.NewReader(plain))
}

func (n *Node) deleteBlock(w http.ResponseWriter, r *http.Request, id string) {
	n.storageMu.Lock()
	err := os.Remove(filepath.Join(n.StorageDir, id))
	if err == nil || os.IsNotExist(err) {
		n.removeOwnersLocked(id)
	}
	n.storageMu.Unlock()
	if err != nil && !os.IsNotExist(err) {
		fail(w, http.StatusInternalServerError, "falló eliminación")
		return
	}
	// Idempotente ante pérdida de la respuesta de una eliminación previa.
	w.WriteHeader(http.StatusNoContent)
}

func (n *Node) replicate(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		fail(w, http.StatusMethodNotAllowed, "método no permitido")
		return
	}
	if !n.clusterAuthorized(r) {
		fail(w, http.StatusUnauthorized, "requiere autenticación interna")
		return
	}
	var body struct {
		Target forwardTarget `json:"target"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 8192)).Decode(&body); err != nil || body.Target.Node == "" || body.Target.Node == n.ID || n.validURL(body.Target.URL) != nil {
		fail(w, http.StatusBadRequest, "destino de réplica no válido")
		return
	}
	plain, err := n.readBlock(id)
	if err != nil {
		if os.IsNotExist(err) {
			fail(w, http.StatusNotFound, "bloque no existe")
		} else {
			n.forgetVerified(id)
			fail(w, http.StatusInternalServerError, "bloque corrupto o ilegible")
		}
		return
	}
	if err := n.forwardBlock(r.Context(), body.Target, id, plain, true); err != nil {
		log.Printf("reparación %s -> %s: %v", id, body.Target.Node, err)
		fail(w, http.StatusBadGateway, "falló transferencia de réplica")
		return
	}
	common.JSON(w, http.StatusOK, map[string]any{"block_id": id, "size": len(plain), "stored_on": []string{n.ID, body.Target.Node}})
}

func (n *Node) inventory(w http.ResponseWriter, r *http.Request) {
	if !n.clusterAuthorized(r) {
		fail(w, http.StatusUnauthorized, "requiere autenticación interna")
		return
	}
	if r.Method != http.MethodGet {
		fail(w, http.StatusMethodNotAllowed, "método no permitido")
		return
	}
	blocks, used := n.localInventory()
	common.JSON(w, http.StatusOK, map[string]any{"node_id": n.ID, "blocks": blocks, "used": used, "capacity": n.Capacity})
}
