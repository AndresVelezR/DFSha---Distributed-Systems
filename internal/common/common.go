// Package common reúne lo que comparten ControlNode y DataNode: secretos,
// TLS, el cliente HTTP y los permisos firmados (tokens) con HMAC.
package common

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

type Config struct {
	ClusterKey                string
	SigningKey, EncryptionKey []byte
	TLSCert, TLSKey, TLSCA    string
	Insecure                  bool
}

func Env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
func secret(k string) (string, error) {
	if f := os.Getenv(k + "_FILE"); f != "" {
		b, e := os.ReadFile(f)
		return strings.TrimSpace(string(b)), e
	}
	if v := os.Getenv(k); v != "" {
		return v, nil
	}
	return "", fmt.Errorf("%s o %s_FILE obligatorio", k, k)
}
func LoadConfig() (Config, error) {
	c := Config{TLSCert: Env("TLS_CERT", ".secrets/cluster.crt"), TLSKey: Env("TLS_KEY", ".secrets/cluster.key"), TLSCA: Env("TLS_CA", ".secrets/ca.crt"), Insecure: Env("DFSHA_INSECURE", "false") == "true"}
	var e error
	c.ClusterKey, e = secret("CLUSTER_KEY")
	if e != nil {
		return c, e
	}
	if len(c.ClusterKey) < 32 {
		return c, errors.New("CLUSTER_KEY debe tener al menos 32 caracteres")
	}
	for _, p := range []struct {
		k   string
		out *[]byte
	}{{"SIGNING_KEY", &c.SigningKey}, {"ENCRYPTION_KEY", &c.EncryptionKey}} {
		s, err := secret(p.k)
		if err != nil {
			return c, err
		}
		*p.out, err = hex.DecodeString(s)
		if err != nil || len(*p.out) != 32 {
			return c, fmt.Errorf("%s debe tener 64 dígitos hexadecimales", p.k)
		}
	}
	if !c.Insecure {
		if _, e = TLSConfig(c, false, false); e != nil {
			return c, e
		}
	}
	return c, nil
}
func TLSConfig(c Config, server, mutual bool) (*tls.Config, error) {
	cert, e := tls.LoadX509KeyPair(c.TLSCert, c.TLSKey)
	if e != nil {
		return nil, e
	}
	ca, e := os.ReadFile(c.TLSCA)
	if e != nil {
		return nil, e
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(ca) {
		return nil, errors.New("CA inválida")
	}
	cfg := &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{cert}, RootCAs: pool, ClientCAs: pool}
	if server && mutual {
		cfg.ClientAuth = tls.RequireAndVerifyClientCert
	}
	return cfg, nil
}

// HTTPClient es el cliente para hablar con otros nodos. Verifica el
// certificado con la CA del clúster y no espera más de 5 s para conectar,
// para que una máquina apagada se detecte rápido en vez de colgar al nodo.
func HTTPClient(c Config) *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.DialContext = (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext
	if !c.Insecure {
		tr.TLSClientConfig, _ = TLSConfig(c, false, false)
	}
	return &http.Client{Transport: tr, Timeout: 120 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error { return http.ErrUseLastResponse }}
}
func Serve(addr string, h http.Handler, c Config) error {
	srv := &http.Server{Addr: addr, Handler: h, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 180 * time.Second, WriteTimeout: 180 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 1 << 20}
	if c.Insecure {
		return srv.ListenAndServe()
	}
	return srv.ListenAndServeTLS(c.TLSCert, c.TLSKey)
}

type Claims struct {
	Kind    string `json:"kind"`
	User    string `json:"user,omitempty"`
	Block   string `json:"block,omitempty"`
	Node    string `json:"node,omitempty"`
	Exp     int64  `json:"exp"`
	MaxSize int64  `json:"max_size,omitempty"`
}

func Sign(c Claims, key []byte) string {
	b, _ := json.Marshal(c)
	p := base64.RawURLEncoding.EncodeToString(b)
	h := hmac.New(sha256.New, key)
	h.Write([]byte(p))
	return p + "." + base64.RawURLEncoding.EncodeToString(h.Sum(nil))
}
func Verify(token string, key []byte) (Claims, error) {
	var c Claims
	p := strings.Split(token, ".")
	if len(p) != 2 || len(token) > 4096 {
		return c, errors.New("token inválido")
	}
	sig, e := base64.RawURLEncoding.DecodeString(p[1])
	if e != nil {
		return c, e
	}
	h := hmac.New(sha256.New, key)
	h.Write([]byte(p[0]))
	if !hmac.Equal(sig, h.Sum(nil)) {
		return c, errors.New("firma inválida")
	}
	b, e := base64.RawURLEncoding.DecodeString(p[0])
	if e != nil {
		return c, e
	}
	if e = json.Unmarshal(b, &c); e != nil {
		return c, e
	}
	if c.Exp <= time.Now().Unix() {
		return c, errors.New("token vencido")
	}
	return c, nil
}
func ClusterAuth(r *http.Request, key string) bool {
	return subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Cluster-Key")), []byte(key)) == 1
}
func JSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if status != 204 {
		_ = json.NewEncoder(w).Encode(v)
	}
}
func ValidID(id string) bool {
	if len(id) != 64 {
		return false
	}
	b, e := hex.DecodeString(id)
	return e == nil && len(b) == 32 && strings.ToLower(id) == id
}
