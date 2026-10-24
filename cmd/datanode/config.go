package main

import (
	"errors"
	"net/url"
	"strconv"
	"strings"
	"time"

	"dfsha/internal/common"
)

func loadNode() (*Node, error) {
	cfg, err := common.LoadConfig()
	if err != nil {
		return nil, err
	}
	capacity, err := strconv.ParseInt(common.Env("CAPACITY_BYTES", "1073741824"), 10, 64)
	if err != nil || capacity < maxBlockSize {
		return nil, errors.New("CAPACITY_BYTES debe ser un entero >= 67108864")
	}
	scheme := "https"
	if cfg.Insecure {
		scheme = "http"
	}
	n := &Node{
		ID:         common.Env("NODE_ID", "dn-01"),
		URL:        common.Env("ADVERTISE_URL", scheme+"://localhost:8001"),
		Addr:       common.Env("DN_ADDR", ":8001"),
		StorageDir: common.Env("STORAGE_DIR", "/data"),
		Capacity:   capacity, Config: cfg, Client: common.HTTPClient(cfg),
	}
	for _, raw := range strings.Split(common.Env("CONTROL_URLS", scheme+"://localhost:8000"), ",") {
		base := strings.TrimRight(strings.TrimSpace(raw), "/")
		if err := n.validURL(base); err != nil {
			return nil, err
		}
		n.ControlURLs = append(n.ControlURLs, base)
	}
	if n.ID == "" || n.validURL(n.URL) != nil {
		return nil, errors.New("NODE_ID y ADVERTISE_URL válidos requeridos")
	}
	return n, nil
}

func (n *Node) validURL(base string) error {
	u, err := url.Parse(base)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return errors.New("URL de nodo no válida")
	}
	if u.Scheme != "https" && !(n.Config.Insecure && u.Scheme == "http") {
		return errors.New("la URL requiere HTTPS (HTTP sólo con DFSHA_INSECURE=true)")
	}
	return nil
}

func interval(name string, fallback time.Duration) time.Duration {
	v, err := strconv.ParseInt(common.Env(name, "0"), 10, 64)
	if err == nil && v > 0 {
		return time.Duration(v) * time.Millisecond
	}
	return fallback
}
