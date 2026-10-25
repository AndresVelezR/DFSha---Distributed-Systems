package main

import (
	"encoding/json"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"dfsha/internal/common"

	"github.com/hashicorp/raft"
)

func newTestServer(t *testing.T, datanodes int) *Server {
	t.Helper()
	c := Config{Security: common.Config{Insecure: true, SigningKey: []byte(strings.Repeat("s", 32)), ClusterKey: strings.Repeat("k", 32)}, ReplicationFactor: 2, BlockSizing: defaultSizing, HeartbeatInterval: 3 * time.Second, HeartbeatMisses: 3, WriteLeaseTTL: time.Minute, FullThreshold: .9}
	s := newServer(c)
	s.users["demo"] = "test"
	s.users["other"] = "test"
	s.fsm.state.Dirs[key("demo", "/")] = true
	s.fsm.state.Dirs[key("other", "/")] = true
	cfg := raft.DefaultConfig()
	cfg.LocalID = "test"
	cfg.LogOutput = io.Discard
	cfg.HeartbeatTimeout = 50 * time.Millisecond
	cfg.ElectionTimeout = 50 * time.Millisecond
	cfg.LeaderLeaseTimeout = 25 * time.Millisecond
	cfg.CommitTimeout = 5 * time.Millisecond
	addr, tr := raft.NewInmemTransport("test")
	store := raft.NewInmemStore()
	var e error
	s.raft, e = raft.NewRaft(cfg, s.fsm, store, store, raft.NewInmemSnapshotStore(), tr)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.raft.BootstrapCluster(raft.Configuration{Servers: []raft.Server{{ID: "test", Address: addr, Suffrage: raft.Voter}}}).Error(); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { s.raft.Shutdown() })
	until := time.Now().Add(2 * time.Second)
	for s.raft.State() != raft.Leader && time.Now().Before(until) {
		time.Sleep(time.Millisecond * 5)
	}
	if s.raft.State() != raft.Leader {
		t.Fatal("no leader")
	}
	for i := 0; i < datanodes; i++ {
		id := string(rune('a' + i))
		s.nodes[id] = &NodeInfo{NodeID: id, URL: "http://127.0.0.1:1", Capacity: 1000 * megabyte, LastSeen: time.Now()}
	}
	return s
}
func api(s *Server, method, p, body, user string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, p, strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+common.Sign(common.Claims{Kind: "session", User: user, Exp: time.Now().Add(time.Hour).Unix()}, s.cfg.Security.SigningKey))
	rec := httptest.NewRecorder()
	s.routes().ServeHTTP(rec, r)
	return rec
}
func TestUploadReservesPathAndExpiry(t *testing.T) {
	s := newTestServer(t, 3)
	body := `{"path":"/a","size":20000000,"sha256":"` + strings.Repeat("a", 64) + `"}`
	if r := api(s, "POST", "/files/upload/plan", body, "demo"); r.Code != 200 {
		t.Fatal(r.Code, r.Body)
	}
	if r := api(s, "POST", "/files/upload/plan", body, "demo"); r.Code != 409 {
		t.Fatal(r.Code, r.Body)
	}
	v := s.state()
	for _, up := range v.Uploads {
		up.ExpiresAt = time.Now().Add(-time.Second)
	}
	if e := s.save(v); e != nil {
		t.Fatal(e)
	}
	if r := api(s, "POST", "/files/upload/plan", body, "demo"); r.Code != 200 {
		t.Fatal(r.Code, r.Body)
	}
}
func TestIncompleteCommitInvisible(t *testing.T) {
	s := newTestServer(t, 3)
	r := api(s, "POST", "/files/upload/plan", `{"path":"/a","size":20000000,"sha256":"`+strings.Repeat("a", 64)+`"}`, "demo")
	var up UploadSession
	json.Unmarshal(r.Body.Bytes(), &up)
	r = api(s, "POST", "/files/upload/commit", `{"upload_id":"`+up.UploadID+`","sha256":"`+up.SHA256+`","blocks":[]}`, "demo")
	if r.Code != 409 {
		t.Fatal(r.Code, r.Body)
	}
	if api(s, "GET", "/fs/stat?path=/a", "", "demo").Code != 404 {
		t.Fatal("incomplete visible")
	}
}
func TestCommitDoesNotTrustClientReplicaClaims(t *testing.T) {
	s := newTestServer(t, 3)
	up := &UploadSession{UploadID: "up", Owner: "demo", Path: "/a", Size: 1, SHA256: strings.Repeat("a", 64), BlockSize: 8 * megabyte, NBlocks: 1, ExpiresAt: time.Now().Add(time.Minute), Blocks: []BlockPlan{{Index: 0, Targets: []Target{{Node: "a"}, {Node: "b"}}}}}
	v := s.state()
	v.Uploads[up.UploadID] = up
	s.save(v)
	r := api(s, "POST", "/files/upload/commit", `{"upload_id":"up","sha256":"`+up.SHA256+`","blocks":[{"index":0,"block_id":"`+strings.Repeat("b", 64)+`","size":1,"stored_on":["a","b"]}]}`, "demo")
	if r.Code != 409 {
		t.Fatal(r.Code, r.Body)
	}
}
func TestFSMRoundTrip(t *testing.T) {
	s := newTestServer(t, 0)
	r := api(s, "POST", "/fs/mkdir", `{"path":"/persistent"}`, "demo")
	if r.Code != 201 {
		t.Fatal(r.Code)
	}
	snap, e := s.fsm.Snapshot()
	if e != nil {
		t.Fatal(e)
	}
	data := snap.(*snapshot).data
	other := &metadataFSM{}
	if e = other.Restore(io.NopCloser(strings.NewReader(string(data)))); e != nil {
		t.Fatal(e)
	}
	if !other.state.Dirs[key("demo", "/persistent")] {
		t.Fatal("lost metadata")
	}
}
