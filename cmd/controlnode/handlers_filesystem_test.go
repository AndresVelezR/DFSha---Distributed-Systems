package main

import (
	"testing"
	"time"
)

func TestLiveReplicasIgnoresDeadNodes(t *testing.T) {
	s := newTestServer(t, 3)
	blocks := []StoredBlock{
		{Index: 0, StoredOn: []string{"dn-01", "dn-02"}},
		{Index: 1, StoredOn: []string{"dn-02", "dn-03"}},
	}
	if got := s.liveReplicas(blocks); got != 2 {
		t.Fatalf("con todos los nodos vivos: %d copias vivas, se esperaban 2", got)
	}

	s.nodes["dn-01"].LastSeen = time.Now().Add(-time.Minute) // dn-01 deja de mandar heartbeat
	if got := s.liveReplicas(blocks); got != 1 {
		t.Fatalf("con dn-01 caído: %d copias vivas, se esperaba 1 (el bloque 0 quedó con una)", got)
	}
}
