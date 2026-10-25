package main

import (
	"net/http"

	"dfsha/internal/common"
)

func writeJSON(w http.ResponseWriter, status int, v any) { common.JSON(w, status, v) }

func fail(w http.ResponseWriter, status int, msg string) {
	common.JSON(w, status, map[string]string{"error": msg})
}
