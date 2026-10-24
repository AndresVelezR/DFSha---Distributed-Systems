package common

import (
	"strings"
	"testing"
	"time"
)

func TestCapabilitiesDetectTamperingExpiryAndWrongKey(t *testing.T) {
	key := []byte(strings.Repeat("a", 32))
	c := Claims{Kind: "block-read", User: "alice", Block: strings.Repeat("b", 64), Node: "dn-01", Exp: time.Now().Add(time.Minute).Unix()}
	token := Sign(c, key)
	got, err := Verify(token, key)
	if err != nil || got != c {
		t.Fatalf("roundtrip: %+v %v", got, err)
	}
	if _, err = Verify(token, []byte(strings.Repeat("z", 32))); err == nil {
		t.Fatal("wrong key accepted")
	}
	if _, err = Verify(token[:len(token)-4]+"AAAA", key); err == nil {
		t.Fatal("tampered token accepted")
	}
	c.Exp = time.Now().Add(-time.Second).Unix()
	if _, err = Verify(Sign(c, key), key); err == nil {
		t.Fatal("expired token accepted")
	}
	for _, bad := range []string{"", "a.b.c", strings.Repeat("x", 5000)} {
		if _, err = Verify(bad, key); err == nil {
			t.Fatal("malformed token accepted")
		}
	}
}

func TestIdentifiersDoNotAcceptPathsOrUppercase(t *testing.T) {
	if !ValidID(strings.Repeat("a", 64)) {
		t.Fatal("valid sha rejected")
	}
	for _, id := range []string{"../secret", strings.Repeat("G", 64), strings.Repeat("A", 64), strings.Repeat("a", 63)} {
		if ValidID(id) {
			t.Fatal("unsafe id", id)
		}
	}
}

func TestLoadConfigFailsClosedWithoutSecrets(t *testing.T) {
	for _, name := range []string{"CLUSTER_KEY", "CLUSTER_KEY_FILE", "SIGNING_KEY", "SIGNING_KEY_FILE", "ENCRYPTION_KEY", "ENCRYPTION_KEY_FILE"} {
		t.Setenv(name, "")
	}
	t.Setenv("DFSHA_INSECURE", "true")
	if _, err := LoadConfig(); err == nil {
		t.Fatal("missing secrets accepted")
	}
}
