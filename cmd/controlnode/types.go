package main

import "time"

type NodeInfo struct {
	NodeID   string           `json:"node_id"`
	URL      string           `json:"url"`
	Host     string           `json:"host,omitempty"`
	Port     int              `json:"port,omitempty"`
	Capacity int64            `json:"capacity"`
	Used     int64            `json:"used"`
	LastSeen time.Time        `json:"last_seen"`
	Blocks   map[string]int64 `json:"-"`
}
type Target struct {
	Node  string `json:"node"`
	URL   string `json:"url"`
	Token string `json:"token,omitempty"`
	Host  string `json:"host,omitempty"`
	Port  int    `json:"port,omitempty"`
}
type BlockPlan struct {
	Index   int      `json:"index"`
	Targets []Target `json:"targets"`
}
type UploadSession struct {
	UploadID  string      `json:"upload_id"`
	Owner     string      `json:"owner"`
	Path      string      `json:"path"`
	Size      int64       `json:"size"`
	SHA256    string      `json:"sha256"`
	BlockSize int64       `json:"block_size"`
	NBlocks   int         `json:"n_blocks"`
	ExpiresAt time.Time   `json:"expires_at"`
	Blocks    []BlockPlan `json:"blocks"`
	Handle    string      `json:"handle,omitempty"`
}
type StoredBlock struct {
	Index    int      `json:"index"`
	BlockID  string   `json:"block_id"`
	Size     int64    `json:"size"`
	StoredOn []string `json:"stored_on"`
}
type FileMeta struct {
	Path      string        `json:"path"`
	Owner     string        `json:"owner"`
	Size      int64         `json:"size"`
	SHA256    string        `json:"sha256"`
	BlockSize int64         `json:"block_size"`
	Blocks    []StoredBlock `json:"blocks"`
	CreatedAt time.Time     `json:"created_at"`
}
type Handle struct {
	ID        string    `json:"handle"`
	Owner     string    `json:"owner"`
	Path      string    `json:"path"`
	Mode      string    `json:"mode"`
	ExpiresAt time.Time `json:"expires_at"`
	File      *FileMeta `json:"file,omitempty"`
}
type State struct {
	Revision  uint64                    `json:"revision"`
	Dirs      map[string]bool           `json:"dirs"`
	Files     map[string]*FileMeta      `json:"files"`
	Uploads   map[string]*UploadSession `json:"uploads"`
	Handles   map[string]*Handle        `json:"handles"`
	Completed map[string]*FileMeta      `json:"completed"`
}

func emptyState() *State {
	return &State{Dirs: map[string]bool{}, Files: map[string]*FileMeta{}, Uploads: map[string]*UploadSession{}, Handles: map[string]*Handle{}, Completed: map[string]*FileMeta{}}
}
func key(owner, p string) string { return owner + ":" + p }
