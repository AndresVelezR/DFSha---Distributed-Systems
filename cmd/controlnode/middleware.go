package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"dfsha/internal/common"
)

type userContext struct{}

func owner(r *http.Request) string { return r.Context().Value(userContext{}).(string) }

// withAuth valida el token de sesión del cliente y deja el usuario en el
// contexto. Luego toma el turno (op) y exige que este nodo sea el líder.
func (s *Server) withAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, e := common.Verify(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), s.cfg.Security.SigningKey)
		if e != nil || c.Kind != "session" || s.users[c.User] == "" {
			fail(w, 401, "token inválido o vencido")
			return
		}
		s.op.Lock()
		defer s.op.Unlock()
		if !s.leader(w) {
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), userContext{}, c.User)))
	}
}

// withClusterKey protege las rutas que usan los DataNodes: exigen la clave
// compartida del clúster, que un cliente no conoce.
func (s *Server) withClusterKey(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !common.ClusterAuth(r, s.cfg.Security.ClusterKey) {
			fail(w, 401, "clave interna inválida")
			return
		}
		s.op.Lock()
		defer s.op.Unlock()
		if !s.leader(w) {
			return
		}
		next(w, r)
	}
}
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 16<<20)
	d := json.NewDecoder(r.Body)
	if e := d.Decode(v); e != nil {
		fail(w, 400, "JSON inválido")
		return false
	}
	return true
}
