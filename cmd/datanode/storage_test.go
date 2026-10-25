package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"dfsha/internal/common"
)

func testNode(t *testing.T, id string) *Node {
	t.Helper()
	cfg := common.Config{ClusterKey: strings.Repeat("c", 32), SigningKey: bytes.Repeat([]byte{1}, 32), EncryptionKey: bytes.Repeat([]byte{2}, 32), Insecure: true}
	return &Node{ID: id, StorageDir: t.TempDir(), Capacity: 1 << 30, Config: cfg, Client: common.HTTPClient(cfg)}
}

func capability(n *Node, kind, block, node string, limit int64) string {
	return common.Sign(common.Claims{Kind: kind, User: "alice", Block: block, Node: node, MaxSize: limit, Exp: time.Now().Add(time.Minute).Unix()}, n.Config.SigningKey)
}

func request(n *Node, method, path string, data []byte, token string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, bytes.NewReader(data))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	result := httptest.NewRecorder()
	n.routes().ServeHTTP(result, req)
	return result
}

func internal(n *Node) map[string]string {
	return map[string]string{"X-Cluster-Key": n.Config.ClusterKey}
}

func requireCode(t *testing.T, got *httptest.ResponseRecorder, want int) {
	t.Helper()
	if got.Code != want {
		t.Fatalf("status=%d want=%d body=%s", got.Code, want, got.Body.String())
	}
}

func TestEncryptedStoreAndAtomicConcurrentRetry(t *testing.T) {
	n := testNode(t, "dn-01")
	content := bytes.Repeat([]byte("binary\x00data"), 1024)
	id := hashID(content)
	var wg sync.WaitGroup
	var mu sync.Mutex
	created := 0
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			yes, err := n.store(id, content)
			if err != nil {
				t.Errorf("store: %v", err)
				return
			}
			if yes {
				mu.Lock()
				created++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if created != 1 {
		t.Fatalf("created=%d, want 1", created)
	}
	sealed, err := os.ReadFile(filepath.Join(n.StorageDir, id))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(sealed, content) || !bytes.HasPrefix(sealed, blockMagic) {
		t.Fatal("block not encrypted in expected format")
	}
	another, err := n.encrypt(id, content)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(sealed, another) {
		t.Fatal("AES-GCM nonce reused")
	}
	if err := os.WriteFile(filepath.Join(n.StorageDir, ".block-interrupted"), []byte("partial"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(n.StorageDir, "legacy-file.tmp"), []byte("partial"), 0600); err != nil {
		t.Fatal(err)
	}
	blocks, used := n.localInventory()
	if len(blocks) != 1 || used != int64(len(content)) || blocks[0].BlockID != id {
		t.Fatalf("inventory=%v used=%d", blocks, used)
	}
	entries, err := os.ReadDir(n.StorageDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Fatalf("unremoved write temporaries: %v", entries)
	}
}

func TestCapabilitiesScopeAndExpiry(t *testing.T) {
	n := testNode(t, "dn-01")
	content := []byte("scope-protected block")
	id := hashID(content)
	goodWrite := capability(n, "block-write", id, n.ID, maxBlockSize)
	requireCode(t, request(n, http.MethodPut, "/blocks/"+id, content, goodWrite, nil), http.StatusCreated)
	tests := []struct{ name, token string }{
		{"missing", ""},
		{"wrong node", capability(n, "block-read", id, "dn-02", 0)},
		{"wrong block", capability(n, "block-read", hashID([]byte("other")), n.ID, 0)},
		{"write cannot read", goodWrite},
		{"wildcard cannot read", capability(n, "block-read", "*", n.ID, 0)},
		{"forged", goodWrite + "x"},
		{"expired", common.Sign(common.Claims{Kind: "block-read", Node: n.ID, Block: id, Exp: time.Now().Add(-time.Second).Unix()}, n.Config.SigningKey)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			requireCode(t, request(n, http.MethodGet, "/blocks/"+id, nil, tt.token, nil), http.StatusUnauthorized)
		})
	}
	requireCode(t, request(n, http.MethodGet, "/blocks/"+id, nil, capability(n, "block-read", id, n.ID, 0), nil), http.StatusOK)
	requireCode(t, request(n, http.MethodDelete, "/blocks/"+id, nil, goodWrite, nil), http.StatusUnauthorized)
	requireCode(t, request(n, http.MethodGet, "/inventory", nil, goodWrite, nil), http.StatusUnauthorized)
	requireCode(t, request(n, http.MethodDelete, "/blocks/"+id, nil, "", internal(n)), http.StatusNoContent)
	requireCode(t, request(n, http.MethodDelete, "/blocks/"+id, nil, "", internal(n)), http.StatusNoContent)
}

func TestRangeAndHeadVerifyIntegrity(t *testing.T) {
	n := testNode(t, "dn-01")
	content := []byte("0123456789abcdefghijklmnop")
	id := hashID(content)
	requireCode(t, request(n, http.MethodPut, "/blocks/"+id, content, "", internal(n)), http.StatusCreated)
	token := capability(n, "block-read", id, n.ID, 0)
	got := request(n, http.MethodGet, "/blocks/"+id, nil, token, map[string]string{"Range": "bytes=3-8"})
	requireCode(t, got, http.StatusPartialContent)
	if got.Body.String() != "345678" || got.Header().Get("Content-Range") != "bytes 3-8/26" {
		t.Fatalf("range response: %s %v", got.Body.String(), got.Header())
	}
	got = request(n, http.MethodHead, "/blocks/"+id, nil, token, nil)
	requireCode(t, got, http.StatusOK)
	if got.Body.Len() != 0 || got.Header().Get("Content-Length") != strconv.Itoa(len(content)) || got.Header().Get("X-Block-SHA256") != id {
		t.Fatalf("HEAD response: %s %v", got.Body.String(), got.Header())
	}
	path := filepath.Join(n.StorageDir, id)
	sealed, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sealed[len(sealed)-1] ^= 1
	if err := os.WriteFile(path, sealed, 0600); err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		requireCode(t, request(n, method, "/blocks/"+id, nil, token, nil), http.StatusInternalServerError)
	}
	blocks, used := n.localInventory()
	if len(blocks) != 0 || used != 0 {
		t.Fatalf("corrupt block announced: %v %d", blocks, used)
	}
	requireCode(t, request(n, http.MethodPut, "/blocks/"+id, content, "", internal(n)), http.StatusCreated)
	got = request(n, http.MethodGet, "/blocks/"+id, nil, token, nil)
	requireCode(t, got, http.StatusOK)
	if !bytes.Equal(got.Body.Bytes(), content) {
		t.Fatal("correct replica failed to repair corruption")
	}
}

func TestWriteValidationAndCapacity(t *testing.T) {
	n := testNode(t, "dn-01")
	data := []byte("correct bytes")
	id := hashID(data)
	requireCode(t, request(n, http.MethodPut, "/blocks/"+id, []byte("wrong"), "", internal(n)), http.StatusConflict)
	requireCode(t, request(n, http.MethodPut, "/blocks/"+id, data, capability(n, "block-write", id, n.ID, 3), nil), http.StatusRequestEntityTooLarge)
	req := httptest.NewRequest(http.MethodPut, "/blocks/"+id, strings.NewReader("never read"))
	req.ContentLength = maxBlockSize + 1
	req.Header.Set("X-Cluster-Key", n.Config.ClusterKey)
	recorder := httptest.NewRecorder()
	n.routes().ServeHTTP(recorder, req)
	requireCode(t, recorder, http.StatusRequestEntityTooLarge)
	// A chunked upload without a declared size still enforces its capability.
	req = httptest.NewRequest(http.MethodPut, "/blocks/"+id, bytes.NewReader(data))
	req.ContentLength = -1
	req.Header.Set("Authorization", "Bearer "+capability(n, "block-write", id, n.ID, 3))
	recorder = httptest.NewRecorder()
	n.routes().ServeHTTP(recorder, req)
	requireCode(t, recorder, http.StatusRequestEntityTooLarge)
	n.Capacity = int64(len(data)) - 1
	requireCode(t, request(n, http.MethodPut, "/blocks/"+id, data, "", internal(n)), http.StatusInsufficientStorage)
	for _, bad := range []string{strings.Repeat("z", 64), strings.ToUpper(id), id + "/extra", "../" + id} {
		got := request(n, http.MethodPut, "/blocks/"+bad, data, "", internal(n))
		if got.Code < 300 {
			t.Fatalf("invalid path accepted: %s", bad)
		}
	}
	blocks, _ := n.localInventory()
	if len(blocks) != 0 {
		t.Fatal("invalid writes published blocks")
	}
}

func TestIdempotentUploadAlwaysReplicates(t *testing.T) {
	first := testNode(t, "dn-01")
	second := testNode(t, "dn-02")
	server := httptest.NewServer(second.routes())
	defer server.Close()
	content := []byte("block that must be forwarded on retry")
	id := hashID(content)
	write := capability(first, "block-write", "*", first.ID, maxBlockSize)
	requireCode(t, request(first, http.MethodPut, "/blocks/"+id, content, write, nil), http.StatusCreated)
	header, err := json.Marshal([]forwardTarget{{Node: second.ID, URL: server.URL, Token: capability(second, "block-write", "*", second.ID, maxBlockSize)}})
	if err != nil {
		t.Fatal(err)
	}
	got := request(first, http.MethodPut, "/blocks/"+id, content, write, map[string]string{"X-Forward-To": string(header)})
	requireCode(t, got, http.StatusOK)
	var ack struct {
		Stored []string `json:"stored_on"`
	}
	if err := json.Unmarshal(got.Body.Bytes(), &ack); err != nil {
		t.Fatal(err)
	}
	if len(ack.Stored) != 2 || ack.Stored[1] != second.ID {
		t.Fatalf("replica missing: %s", got.Body.String())
	}
	read := request(second, http.MethodGet, "/blocks/"+id, nil, "", internal(second))
	requireCode(t, read, http.StatusOK)
	if !bytes.Equal(content, read.Body.Bytes()) {
		t.Fatal("replica corrupted")
	}
	// Existing blocks must still validate every retry's body.
	requireCode(t, request(first, http.MethodPut, "/blocks/"+id, []byte("bad retry"), write, nil), http.StatusConflict)
}

func TestInternalReplicationAndInventory(t *testing.T) {
	source := testNode(t, "dn-01")
	target := testNode(t, "dn-02")
	server := httptest.NewServer(target.routes())
	defer server.Close()
	content := []byte("control-initiated binary repair")
	id := hashID(content)
	if _, err := source.store(id, content); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]any{"target": forwardTarget{Node: target.ID, URL: server.URL}})
	requireCode(t, request(source, http.MethodPost, "/blocks/"+id+"/replicate", body, "", nil), http.StatusUnauthorized)
	requireCode(t, request(source, http.MethodPost, "/blocks/"+id+"/replicate", body, "", internal(source)), http.StatusOK)
	got := request(target, http.MethodGet, "/inventory", nil, "", internal(target))
	requireCode(t, got, http.StatusOK)
	var inventory struct {
		Blocks   []blockInfo `json:"blocks"`
		Used     int64       `json:"used"`
		Capacity int64       `json:"capacity"`
	}
	if err := json.Unmarshal(got.Body.Bytes(), &inventory); err != nil {
		t.Fatal(err)
	}
	if len(inventory.Blocks) != 1 || inventory.Blocks[0].BlockID != id || inventory.Used != int64(len(content)) || inventory.Capacity != target.Capacity {
		t.Fatalf("inventory: %s", got.Body.String())
	}
}

func TestForwardRejectsUnsignedTargetAndReportsFailure(t *testing.T) {
	n := testNode(t, "dn-01")
	content := []byte("data")
	id := hashID(content)
	requireCode(t, request(n, http.MethodPut, "/blocks/"+id, content, "", map[string]string{"X-Cluster-Key": n.Config.ClusterKey, "X-Forward-To": `[{"node":"dn-02","url":"http://localhost:1"}]`}), http.StatusBadRequest)
	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) }))
	defer failing.Close()
	header, _ := json.Marshal([]forwardTarget{{Node: "dn-02", URL: failing.URL, Token: capability(n, "block-write", "*", "dn-02", maxBlockSize)}})
	got := request(n, http.MethodPut, "/blocks/"+id, content, "", map[string]string{"X-Cluster-Key": n.Config.ClusterKey, "X-Forward-To": string(header)})
	requireCode(t, got, http.StatusCreated)
	var ack struct {
		Stored []string `json:"stored_on"`
	}
	if err := json.Unmarshal(got.Body.Bytes(), &ack); err != nil {
		t.Fatal(err)
	}
	if len(ack.Stored) != 1 {
		t.Fatalf("claimed unavailable replica: %s", got.Body.String())
	}
}

func TestReportsReachEveryControlAndTolerateFollower(t *testing.T) {
	n := testNode(t, "dn-01")
	var mu sync.Mutex
	got := map[string]int{}
	makeControl := func(name string, code int) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("X-Cluster-Key") != n.Config.ClusterKey {
				t.Error("missing cluster authentication")
			}
			raw, err := io.ReadAll(r.Body)
			if err != nil {
				t.Error(err)
			}
			var body map[string]any
			if json.Unmarshal(raw, &body) != nil || body["node_id"] != n.ID {
				t.Errorf("invalid report: %s", raw)
			}
			mu.Lock()
			got[name+r.URL.Path]++
			mu.Unlock()
			w.WriteHeader(code)
		}))
	}
	follower := makeControl("follower", http.StatusServiceUnavailable)
	leader := makeControl("leader", http.StatusOK)
	defer follower.Close()
	defer leader.Close()
	n.ControlURLs = []string{follower.URL, leader.URL}
	n.URL = "http://localhost:8001"
	if err := n.register(); err != nil {
		t.Fatal(err)
	}
	if err := n.blockReport(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"leader", "follower"} {
		for _, path := range []string{"/cluster/register", "/cluster/blockreport"} {
			if got[name+path] != 1 {
				t.Fatalf("report %s%s count=%d", name, path, got[name+path])
			}
		}
	}
}

// El inventario reutiliza la verificación de un bloque mientras su archivo no
// cambie, pero vuelve a verificar y excluye una copia alterada en disco.
func TestInventoryReverifiesChangedBlock(t *testing.T) {
	n := testNode(t, "dn-01")
	content := []byte("bloque que luego se altera en disco")
	id := hashID(content)
	if _, err := n.store(id, content); err != nil {
		t.Fatal(err)
	}
	if blocks, used := n.localInventory(); len(blocks) != 1 || used != int64(len(content)) {
		t.Fatalf("inventario inicial: %v %d", blocks, used)
	}
	path := filepath.Join(n.StorageDir, id)
	sealed, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sealed[len(sealed)-1] ^= 1
	if err := os.WriteFile(path, sealed, 0600); err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(time.Second)
	if err := os.Chtimes(path, later, later); err != nil {
		t.Fatal(err)
	}
	if blocks, used := n.localInventory(); len(blocks) != 0 || used != 0 {
		t.Fatalf("el bloque alterado siguió en el inventario: %v %d", blocks, used)
	}
}
