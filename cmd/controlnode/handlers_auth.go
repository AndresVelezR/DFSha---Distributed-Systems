package main

import (
	"dfsha/internal/common"
	"golang.org/x/crypto/bcrypt"
	"net/http"
	"time"
)

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var q struct{ Username, Password string }
	if !decode(w, r, &q) {
		return
	}
	hash := s.users[q.Username]
	if hash == "" {
		hash = "$2a$10$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy"
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(q.Password)) != nil || s.users[q.Username] == "" {
		fail(w, 401, "credenciales inválidas")
		return
	}
	s.op.Lock()
	defer s.op.Unlock()
	if !s.leader(w) {
		return
	}
	v := s.state()
	if !v.Dirs[key(q.Username, "/")] {
		v.Dirs[key(q.Username, "/")] = true
		if !s.persist(w, v) {
			return
		}
	}
	common.JSON(w, 200, map[string]any{"token": common.Sign(common.Claims{Kind: "session", User: q.Username, Exp: time.Now().Add(time.Hour).Unix()}, s.cfg.Security.SigningKey), "expires_in": 3600})
}
