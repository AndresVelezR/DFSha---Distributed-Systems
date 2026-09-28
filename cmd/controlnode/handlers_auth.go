package main

import (
	"encoding/json"
	"net/http"
)

// login valida usuario y contraseña y entrega el token. En este hito hay un
// único usuario de demostración; el control de acceso real llega en el Hito 3.
func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var req struct{ Username, Password string }
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		badRequest(w, err)
		return
	}
	if req.Username != demoUser || req.Password != demoPassword {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "credenciales inválidas"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"token": demoToken, "expires_in": 3600})
}
