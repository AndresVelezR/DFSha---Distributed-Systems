package main

import (
	"dfsha/internal/common"
	"net/http"
)

func writeJSON(w http.ResponseWriter, status int, v any) { common.JSON(w, status, v) }
