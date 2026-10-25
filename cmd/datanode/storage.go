package main

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"log"
	"os"
	"path/filepath"
	"time"
)

var blockMagic = []byte("DFSHA1\x00")
var errCapacity = errors.New("capacidad insuficiente")

func validID(id string) bool {
	if len(id) != sha256.Size*2 {
		return false
	}
	for _, c := range id {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

func hashID(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// store guarda un bloque cifrado. El mutex protege la decisión de escribir y
// el inventario; nunca se conserva durante transferencias de red. El bloque se
// escribe a un temporal y se renombra al final, así que un bloque nunca queda
// visible a medias y las lecturas no necesitan el mutex.
func (n *Node) store(id string, plain []byte) (bool, error) {
	n.storageMu.Lock()
	defer n.storageMu.Unlock()
	p := filepath.Join(n.StorageDir, id)
	if existing, err := n.readBlock(id); err == nil {
		if !bytes.Equal(existing, plain) {
			return false, errors.New("conflicto con bloque inmutable")
		}
		return false, nil
	} else if !os.IsNotExist(err) {
		log.Printf("reparando copia corrupta %s: %v", id, err)
	}
	_, used := n.inventoryLocked()
	if int64(len(plain)) > n.Capacity-used {
		return false, errCapacity
	}
	sealed, err := n.encrypt(id, plain)
	if err != nil {
		return false, err
	}
	tmp, err := os.CreateTemp(n.StorageDir, ".block-*")
	if err != nil {
		return false, err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0600); err != nil {
		_ = tmp.Close()
		return false, err
	}
	_, writeErr := tmp.Write(sealed)
	if writeErr == nil {
		writeErr = tmp.Sync()
	}
	closeErr := tmp.Close()
	if writeErr != nil {
		return false, writeErr
	}
	if closeErr != nil {
		return false, closeErr
	}
	// Windows no permite renombrar sobre un archivo existente. Sólo puede
	// existir una copia corrupta: la válida ya devolvió arriba bajo el mutex.
	if _, err := os.Stat(p); err == nil {
		if err := os.Remove(p); err != nil {
			return false, err
		}
	}
	if err := os.Rename(tmpName, p); err != nil {
		return false, err
	}
	// fsync de directorio en Unix. Windows lo rechaza; el archivo ya se
	// sincronizó antes de publicar en ambas plataformas.
	if dir, err := os.Open(n.StorageDir); err == nil {
		_ = dir.Sync()
		_ = dir.Close()
	}
	return true, nil
}

func (n *Node) gcm() (cipher.AEAD, error) {
	if len(n.Config.EncryptionKey) != 32 {
		return nil, errors.New("clave AES-256 no válida")
	}
	block, err := aes.NewCipher(n.Config.EncryptionKey)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func (n *Node) encrypt(id string, plain []byte) ([]byte, error) {
	gcm, err := n.gcm()
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	sealed := append(append([]byte{}, blockMagic...), nonce...)
	return gcm.Seal(sealed, nonce, plain, []byte(id)), nil
}

// readBlock lee, descifra y verifica un bloque. Un bloque publicado no cambia
// nunca, así que se puede leer sin el mutex.
func (n *Node) readBlock(id string) ([]byte, error) {
	f, err := os.Open(filepath.Join(n.StorageDir, id))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	gcm, err := n.gcm()
	if err != nil {
		return nil, err
	}
	maxSealed := maxBlockSize + int64(len(blockMagic)+gcm.NonceSize()+gcm.Overhead())
	sealed, err := io.ReadAll(io.LimitReader(f, maxSealed+1))
	if err != nil {
		return nil, err
	}
	start := len(blockMagic)
	if int64(len(sealed)) > maxSealed || len(sealed) < start+gcm.NonceSize()+gcm.Overhead() || !bytes.Equal(sealed[:start], blockMagic) {
		return nil, errors.New("formato de bloque cifrado no válido")
	}
	nonce := sealed[start : start+gcm.NonceSize()]
	plain, err := gcm.Open(nil, nonce, sealed[start+gcm.NonceSize():], []byte(id))
	if err != nil {
		return nil, errors.New("autenticación AES-GCM falló")
	}
	if hashID(plain) != id {
		return nil, errors.New("integridad SHA-256 falló")
	}
	return plain, nil
}

func (n *Node) localInventory() ([]blockInfo, int64) {
	n.storageMu.Lock()
	defer n.storageMu.Unlock()
	return n.inventoryLocked()
}

// verifiedFile recuerda que un archivo de bloque ya se descifró y verificó,
// y cómo era en disco en ese momento.
type verifiedFile struct {
	diskSize int64
	modTime  time.Time
	size     int64 // bytes del bloque sin cifrar
}

// inventoryLocked lista los bloques válidos y los bytes que ocupan. Cada
// bloque se verifica completo una sola vez; mientras su archivo no cambie de
// tamaño ni de fecha se reutiliza ese resultado. Así el heartbeat, que pide el
// inventario cada pocos segundos, no vuelve a descifrar todo el disco, y una
// copia alterada en disco se vuelve a verificar y se excluye.
func (n *Node) inventoryLocked() ([]blockInfo, int64) {
	blocks := []blockInfo{}
	var used int64
	entries, err := os.ReadDir(n.StorageDir)
	if err != nil {
		log.Printf("leer inventario %s: %v", n.ID, err)
		return blocks, used
	}
	if n.verified == nil {
		n.verified = map[string]verifiedFile{}
	}
	present := map[string]bool{}
	for _, e := range entries {
		id := e.Name()
		if e.IsDir() || !validID(id) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		present[id] = true
		cached, ok := n.verified[id]
		if !ok || cached.diskSize != info.Size() || !cached.modTime.Equal(info.ModTime()) {
			plain, err := n.readBlock(id)
			if err != nil {
				log.Printf("excluyendo bloque corrupto %s: %v", id, err)
				delete(n.verified, id)
				continue
			}
			cached = verifiedFile{diskSize: info.Size(), modTime: info.ModTime(), size: int64(len(plain))}
			n.verified[id] = cached
		}
		blocks = append(blocks, blockInfo{BlockID: id, Size: cached.size})
		used += cached.size
	}
	for id := range n.verified {
		if !present[id] {
			delete(n.verified, id)
		}
	}
	return blocks, used
}

// forgetVerified obliga a verificar de nuevo un bloque, por ejemplo después
// de que una lectura lo encontró corrupto.
func (n *Node) forgetVerified(id string) {
	n.storageMu.Lock()
	delete(n.verified, id)
	n.storageMu.Unlock()
}
