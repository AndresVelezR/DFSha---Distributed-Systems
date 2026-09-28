package main

import (
	"log"
	"net/http"
	"time"
)

// logging registra cada petición con su duración.
func logging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		log.Printf("%s %s %s", r.Method, r.URL.RequestURI(), time.Since(start))
	})
}

// withAuth exige el token del cliente en la cabecera Authorization.
func (s *Server) withAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+demoToken {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "token inválido"})
			return
		}
		next(w, r)
	}
}

// withClusterKey exige la clave compartida del clúster en X-Cluster-Key.
// La usan los DataNodes para registrarse y enviar heartbeats.
func (s *Server) withClusterKey(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Cluster-Key") != s.cfg.ClusterKey {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "cluster key inválida"})
			return
		}
		next(w, r)
	}
}
