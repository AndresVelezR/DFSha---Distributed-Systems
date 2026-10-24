package main

import (
	"testing"
	"time"
)

func TestRetainedCopiesDoNotCauseRebalanceOscillation(t *testing.T) {
	s := &Server{retired: map[string]retiredReplica{"n:old": {Node: "n", Block: "old", At: time.Now().Add(time.Minute)}}}
	n := &NodeInfo{NodeID: "n", Used: 300, Blocks: map[string]int64{"old": 100, "active": 168}}
	if got := s.balanceUsed(n); got != 168 {
		t.Fatalf("active usage %d, expected168", got)
	}
	if n.Used != 300 {
		t.Fatal("real capacity accounting changed")
	}
	delete(n.Blocks, "old")
	if s.balanceUsed(n) != 300 {
		t.Fatal("missing retained copy subtracted")
	}
}
