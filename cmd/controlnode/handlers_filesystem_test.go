package main

import (
	"encoding/json"
	"testing"
	"time"
)

func TestUserNamespacesAndFileDirectoryCollision(t *testing.T) {
	s := newTestServer(t, 0)
	if r := api(s, "POST", "/fs/mkdir", `{"path":"/private"}`, "demo"); r.Code != 201 {
		t.Fatal(r.Code, r.Body)
	}
	if r := api(s, "GET", "/fs/stat?path=/private", "", "other"); r.Code != 404 {
		t.Fatal("other user saw directory", r.Code)
	}
	if r := api(s, "POST", "/fs/mkdir", `{"path":"/private"}`, "other"); r.Code != 201 {
		t.Fatal("namespace not independent", r.Code)
	}
	if api(s, "DELETE", "/fs/rmdir?path=/", "", "demo").Code != 409 {
		t.Fatal("root deleted")
	}
}
func TestWriterLockAndReaderSnapshot(t *testing.T) {
	s := newTestServer(t, 0)
	v := s.state()
	v.Files[key("demo", "/x")] = &FileMeta{Path: "/x", Owner: "demo", Size: 1}
	s.save(v)
	r := api(s, "POST", "/files/open", `{"path":"/x","mode":"w"}`, "demo")
	if r.Code != 200 {
		t.Fatal(r.Code, r.Body)
	}
	var h Handle
	json.Unmarshal(r.Body.Bytes(), &h)
	if api(s, "POST", "/files/lock", `{"path":"/x"}`, "demo").Code != 409 {
		t.Fatal("two writers")
	}
	if api(s, "DELETE", "/fs/rm?path=/x", "", "demo").Code != 409 {
		t.Fatal("removed locked file")
	}
	if api(s, "POST", "/files/close", `{"handle":"`+h.ID+`"}`, "other").Code != 403 {
		t.Fatal("foreign close")
	}
	if api(s, "POST", "/files/close", `{"handle":"`+h.ID+`"}`, "demo").Code != 204 {
		t.Fatal("close")
	}
	r = api(s, "POST", "/files/open", `{"path":"/x","mode":"r"}`, "demo")
	json.Unmarshal(r.Body.Bytes(), &h)
	v = s.state()
	delete(v.Files, key("demo", "/x"))
	s.save(v)
	if s.state().Handles[h.ID].File.Size != 1 {
		t.Fatal("snapshot lost")
	}
	v = s.state()
	v.Handles[h.ID].ExpiresAt = time.Now().Add(-time.Second)
	s.save(v)
	if api(s, "POST", "/files/renew", `{"handle":"`+h.ID+`"}`, "demo").Code != 410 {
		t.Fatal("expired handle revived")
	}
}
func TestMoveDirectoryAndRejectInvalidPaths(t *testing.T) {
	s := newTestServer(t, 0)
	for _, p := range []string{"/a", "/a/b"} {
		if api(s, "POST", "/fs/mkdir", `{"path":"`+p+`"}`, "demo").Code != 201 {
			t.Fatal("mkdir")
		}
	}
	if api(s, "POST", "/fs/mv", `{"src":"/a","dst":"/z"}`, "demo").Code != 200 {
		t.Fatal("mv")
	}
	if api(s, "GET", "/fs/stat?path=/z/b", "", "demo").Code != 200 {
		t.Fatal("nested dir lost")
	}
	if api(s, "POST", "/fs/mkdir", `{"path":"/bad\\name"}`, "demo").Code != 400 {
		t.Fatal("backslash accepted")
	}
	if api(s, "DELETE", "/fs/rmdir?path=/z", "", "demo").Code != 409 {
		t.Fatal("nonempty removed")
	}
}
