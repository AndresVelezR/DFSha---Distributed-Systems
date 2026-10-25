package main

import (
	"testing"
	"time"
)

// Una copia retirada sigue ocupando disco, pero no debe contar para decidir
// el rebalanceo; si contara, el nodo de origen seguiría pareciendo lleno.
func TestRetainedCopiesDoNotCauseRebalanceOscillation(t *testing.T) {
	s := &Server{retired: map[string]retiredReplica{"n:old": {Node: "n", Block: "old", At: time.Now().Add(time.Minute)}}}
	n := &NodeInfo{NodeID: "n", Used: 300, Blocks: map[string]int64{"old": 100, "active": 200}}
	if got := s.balanceUsed(n); got != 200 {
		t.Fatalf("ocupación activa %d, se esperaba 200", got)
	}
	if n.Used != 300 {
		t.Fatal("cambió la ocupación real del disco")
	}
	delete(n.Blocks, "old")
	if s.balanceUsed(n) != 300 {
		t.Fatal("se descontó una copia retirada que ya no existe")
	}
}

func TestExpireLeasesKeepsLiveUploads(t *testing.T) {
	now := time.Now()
	v := emptyState()
	v.Uploads["vencida"] = &UploadSession{ExpiresAt: now.Add(-time.Second)}
	v.Uploads["viva"] = &UploadSession{ExpiresAt: now.Add(time.Minute)}
	v.Handles["h"] = &Handle{ExpiresAt: now.Add(-time.Second)}
	changed, uploading := expireLeases(v, now)
	if !changed || !uploading {
		t.Fatalf("changed=%v uploading=%v, se esperaban ambos true", changed, uploading)
	}
	if v.Uploads["vencida"] != nil || v.Uploads["viva"] == nil || len(v.Handles) != 0 {
		t.Fatal("no se vencieron exactamente las reservas y handles caducados")
	}
}
