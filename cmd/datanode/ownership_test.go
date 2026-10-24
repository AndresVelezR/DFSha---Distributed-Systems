package main

import (
	"dfsha/internal/common"
	"net/http"
	"testing"
	"time"
)

func TestKnownHashDoesNotGrantAnotherAccountOwnership(t *testing.T) {
	n := testNode(t, "dn-01")
	data := []byte("private content")
	id := hashID(data)
	token := capability(n, "block-write", id, n.ID, 64)
	requireCode(t, request(n, "PUT", "/blocks/"+id, data, token, nil), http.StatusCreated)
	headers := internal(n)
	headers["X-Block-Owner"] = "alice"
	requireCode(t, request(n, "HEAD", "/blocks/"+id, nil, "", headers), http.StatusOK)
	headers["X-Block-Owner"] = "bob"
	requireCode(t, request(n, "HEAD", "/blocks/"+id, nil, "", headers), http.StatusNotFound)
	// A second account must actually present the matching bytes, even if the
	// encrypted physical block already exists due to content deduplication.
	token = common.Sign(common.Claims{Kind: "block-write", User: "bob", Block: id, Node: n.ID, MaxSize: 64, Exp: time.Now().Add(time.Minute).Unix()}, n.Config.SigningKey)
	requireCode(t, request(n, "PUT", "/blocks/"+id, data, token, nil), http.StatusOK)
	requireCode(t, request(n, "HEAD", "/blocks/"+id, nil, "", headers), http.StatusOK)
	recovered := testNode(t, "dn-01")
	recovered.StorageDir = n.StorageDir
	if !recovered.hasOwner(id, "alice") || !recovered.hasOwner(id, "bob") {
		t.Fatal("ownership did not persist")
	}
	requireCode(t, request(n, "DELETE", "/blocks/"+id, nil, "", internal(n)), http.StatusNoContent)
	if n.hasOwner(id, "alice") || n.hasOwner(id, "bob") {
		t.Fatal("deleted block retained ownership records")
	}
}
