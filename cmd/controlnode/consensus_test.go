package main

import (
	"encoding/json"
	"testing"

	"github.com/hashicorp/raft"
)

func TestStaleFullStateCannotEraseConfirmedOperation(t *testing.T) {
	f := &metadataFSM{state: emptyState()}
	first := emptyState()
	first.Revision = 1
	first.Dirs["alice:/confirmed"] = true
	b, _ := json.Marshal(first)
	if got := f.Apply(&raft.Log{Data: b}); got != nil {
		t.Fatal(got)
	}
	// This models a suspended leader regaining leadership with an old snapshot.
	stale := emptyState()
	stale.Revision = 1
	stale.Dirs["alice:/stale"] = true
	b, _ = json.Marshal(stale)
	if got := f.Apply(&raft.Log{Data: b}); got == nil {
		t.Fatal("stale replacement accepted")
	}
	if !f.state.Dirs["alice:/confirmed"] || f.state.Dirs["alice:/stale"] {
		t.Fatal("confirmed metadata was overwritten")
	}
}
func TestRaftAdvertisedDNSRemainsStable(t *testing.T) {
	a := advertisedAddress("cn1:9000")
	if a.String() != "cn1:9000" {
		t.Fatal("advertised hostname changed")
	}
}
