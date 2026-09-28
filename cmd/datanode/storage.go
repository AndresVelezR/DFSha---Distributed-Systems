package main

import (
	"os"
	"strings"
	"syscall"
)

// Acceso al disco local donde viven los bloques.

// usage es lo que el DataNode reporta al ControlNode: la capacidad del volumen
// y los bytes que ocupan sus bloques. La ocupación se mide por bloques y no
// por el espacio usado del disco, porque cuando varios DataNodes comparten un
// disco (Docker en una sola máquina) este refleja todo lo que hay en la
// máquina y no la carga del nodo. En un volumen dedicado a bloques, como el
// del Hito 1, ambas medidas coinciden.
func usage(dir string) (capacity, used int64) {
	_, used = storedBlocks(dir)
	return diskCapacity(dir), used
}

// diskCapacity devuelve el tamaño total del volumen donde está dir, en bytes.
func diskCapacity(dir string) int64 {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 0
	}
	return int64(st.Blocks) * int64(st.Bsize)
}

// storedBlocks cuenta los bloques completos guardados en dir y los bytes que
// ocupan. Ignora los .tmp de escrituras en curso.
func storedBlocks(dir string) (count int, bytes int64) {
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if e.IsDir() || strings.HasSuffix(e.Name(), ".tmp") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		count++
		bytes += info.Size()
	}
	return count, bytes
}

func fileSize(p string) int64 {
	st, err := os.Stat(p)
	if err != nil {
		return 0
	}
	return st.Size()
}
