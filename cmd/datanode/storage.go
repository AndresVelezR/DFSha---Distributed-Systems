package main

import (
	"os"
	"strings"
	"syscall"
)

// Acceso al disco local donde viven los bloques.

// diskUsage devuelve capacidad total y espacio usado del volumen de p, en bytes.
func diskUsage(p string) (capacity, used int64) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(p, &st); err != nil {
		return 0, 0
	}
	capacity = int64(st.Blocks) * int64(st.Bsize)
	free := int64(st.Bavail) * int64(st.Bsize)
	return capacity, capacity - free
}

// countBlocks cuenta los bloques completos guardados (ignora los .tmp en curso).
func countBlocks(dir string) int {
	entries, _ := os.ReadDir(dir)
	count := 0
	for _, e := range entries {
		if !e.IsDir() && !strings.HasSuffix(e.Name(), ".tmp") {
			count++
		}
	}
	return count
}

func fileSize(p string) int64 {
	st, err := os.Stat(p)
	if err != nil {
		return 0
	}
	return st.Size()
}
