package main

import (
	"net/http"
	"time"

	"dfsha/internal/common"

	"golang.org/x/crypto/bcrypt"
)

// login compara la contraseña con su hash bcrypt y entrega un token de sesión
// firmado con HMAC que dura una hora. La primera vez crea el directorio raíz
// del usuario.
func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var q struct{ Username, Password string }
	if !decode(w, r, &q) {
		return
	}
	hash := s.users[q.Username]
	// Con un usuario que no existe también se calcula un bcrypt, para que el
	// tiempo de respuesta no revele qué usuarios existen.
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
