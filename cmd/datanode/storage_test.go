package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStoredBlocksCountsOnlyCompleteBlocks(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, size int) {
		if err := os.WriteFile(filepath.Join(dir, name), make([]byte, size), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("bloque-a", 100)
	write("bloque-b", 50)
	write("bloque-c.tmp", 999) // escritura en curso: no cuenta

	count, bytes := storedBlocks(dir)
	if count != 2 || bytes != 150 {
		t.Fatalf("storedBlocks = (%d bloques, %d bytes), se esperaba (2, 150)", count, bytes)
	}
}
