package main

import (
	"os"
	"path/filepath"
)

// A known SHA alone is not proof that an account owns a block. Record a
// verified PUT by that account; the ControlNode checks this before publication.
// Repair copies do not grant ownership to a different account.
func (n *Node) ownerPath(id, user string) string {
	return filepath.Join(n.StorageDir, ".owners", id, hashID([]byte(user)))
}
func (n *Node) recordOwner(id, user string) error {
	n.storageMu.Lock()
	defer n.storageMu.Unlock()
	p := n.ownerPath(id, user)
	if e := os.MkdirAll(filepath.Dir(p), 0700); e != nil {
		return e
	}
	f, e := os.OpenFile(p, os.O_CREATE|os.O_WRONLY, 0600)
	if e != nil {
		return e
	}
	if e = f.Sync(); e != nil {
		f.Close()
		return e
	}
	return f.Close()
}
func (n *Node) hasOwner(id, user string) bool {
	n.storageMu.Lock()
	defer n.storageMu.Unlock()
	_, e := os.Stat(n.ownerPath(id, user))
	return e == nil
}

func (n *Node) removeOwnersLocked(id string) {
	dir := filepath.Join(n.StorageDir, ".owners", id)
	entries, e := os.ReadDir(dir)
	if e != nil {
		return
	}
	for _, entry := range entries {
		if !entry.IsDir() && validID(entry.Name()) {
			_ = os.Remove(filepath.Join(dir, entry.Name()))
		}
	}
	_ = os.Remove(dir)
}
