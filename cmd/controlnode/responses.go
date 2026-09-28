package main

import (
	"encoding/json"
	"net/http"
)

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func methodNotAllowed(w http.ResponseWriter) {
	writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "método no permitido"})
}

func badRequest(w http.ResponseWriter, err error) {
	writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
}
