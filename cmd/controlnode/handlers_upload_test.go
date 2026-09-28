package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func newTestServer(t *testing.T, datanodes int) *Server {
	t.Helper()
	cfg := Config{
		ClusterKey:        "test-key",
		ReplicationFactor: 2,
		BlockSizing:       defaultSizing,
		HeartbeatInterval: 3 * time.Second,
		HeartbeatMisses:   3,
		WriteLeaseTTL:     60 * time.Second,
	}
	s := newServer(cfg)
	for i := 1; i <= datanodes; i++ {
		id := "dn-0" + string(rune('0'+i))
		s.nodes[id] = &NodeInfo{NodeID: id, Host: id, Port: 8001, Capacity: 1000 * megabyte, LastSeen: time.Now()}
	}
	return s
}

func requestUploadPlan(s *Server, path string) *httptest.ResponseRecorder {
	body := `{"path":"` + path + `","size":20000000}`
	req := httptest.NewRequest(http.MethodPost, "/files/upload/plan", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+demoToken)
	rec := httptest.NewRecorder()
	s.routes().ServeHTTP(rec, req)
	return rec
}

func TestSecondUploadPlanOnSamePathIsRejected(t *testing.T) {
	s := newTestServer(t, 3)

	if rec := requestUploadPlan(s, "/a.bin"); rec.Code != http.StatusOK {
		t.Fatalf("primer plan: código %d, se esperaba 200: %s", rec.Code, rec.Body)
	}
	if rec := requestUploadPlan(s, "/a.bin"); rec.Code != http.StatusConflict {
		t.Fatalf("segundo plan sobre la misma ruta: código %d, se esperaba 409", rec.Code)
	}
	if rec := requestUploadPlan(s, "/b.bin"); rec.Code != http.StatusOK {
		t.Fatalf("plan sobre otra ruta: código %d, se esperaba 200", rec.Code)
	}
}

func TestExpiredReservationIsReleased(t *testing.T) {
	s := newTestServer(t, 3)
	if rec := requestUploadPlan(s, "/a.bin"); rec.Code != http.StatusOK {
		t.Fatalf("primer plan: código %d", rec.Code)
	}
	for _, up := range s.uploads {
		up.ExpiresAt = time.Now().Add(-time.Second) // simula un cliente que murió
	}
	if rec := requestUploadPlan(s, "/a.bin"); rec.Code != http.StatusOK {
		t.Fatalf("plan tras vencer la reserva: código %d, se esperaba 200", rec.Code)
	}
}

func TestUploadPlanCopiesPerBlock(t *testing.T) {
	cases := []struct {
		name      string
		datanodes int
		want      int
	}{
		{"3 DataNodes: 2 copias", 3, 2},
		{"2 DataNodes: N-1 = 1 copia", 2, 1},
	}
	for _, c := range cases {
		s := newTestServer(t, c.datanodes)
		rec := requestUploadPlan(s, "/a.bin")
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: código %d", c.name, rec.Code)
		}
		var plan struct {
			Blocks []BlockPlan `json:"blocks"`
		}
		if err := json.NewDecoder(rec.Body).Decode(&plan); err != nil {
			t.Fatal(err)
		}
		for _, b := range plan.Blocks {
			if len(b.Targets) != c.want {
				t.Errorf("%s: bloque %d con %d copias, se esperaban %d", c.name, b.Index, len(b.Targets), c.want)
			}
		}
	}
}
