// admin initializes local secrets without committing any credential to Git.
package main

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"flag"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

func must(e error) {
	if e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}
func random(n int) string {
	b := make([]byte, n)
	_, e := rand.Read(b)
	must(e)
	return hex.EncodeToString(b)
}
func main() {
	out := flag.String("out", ".secrets", "directorio privado")
	hosts := flag.String("hosts", "localhost,127.0.0.1,cn1,cn2,cn3,dn1,dn2,dn3,dn4", "SAN adicionales separados por coma")
	user := flag.String("user", "demo", "usuario")
	passFile := flag.String("password-file", "", "archivo de contraseña, o aleatoria")
	add := flag.Bool("add-user", false, "agrega usuario sin regenerar claves")
	renew := flag.Bool("renew-cert", false, "reemite certificado con CA existente sin modificar claves de datos ni usuarios")
	flag.Parse()
	if *renew {
		must(renewCertificate(*out, *hosts))
		fmt.Println("Certificado reemitido; reinicia los nodos para cargarlo.")
		return
	}
	if !regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`).MatchString(*user) {
		must(fmt.Errorf("usuario inválido"))
	}
	if !*add {
		if _, e := os.Stat(filepath.Join(*out, "cluster.key")); e == nil {
			must(fmt.Errorf("ya existen secretos en %s; no se sobrescriben", *out))
		}
	}
	must(os.MkdirAll(*out, 0700))
	write := func(name string, b []byte) { must(os.WriteFile(filepath.Join(*out, name), b, 0600)) }
	password := random(16)
	if *passFile != "" {
		b, e := os.ReadFile(*passFile)
		must(e)
		password = strings.TrimSpace(string(b))
	}
	if len(password) < 12 || len(password) > 72 {
		must(fmt.Errorf("la contraseña debe tener entre 12 y 72 bytes"))
	}
	users := map[string]string{}
	if *add {
		b, e := os.ReadFile(filepath.Join(*out, "users.json"))
		must(e)
		must(json.Unmarshal(b, &users))
	}
	hash, e := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	must(e)
	if _, ok := users[*user]; ok {
		must(fmt.Errorf("usuario ya existe"))
	}
	users[*user] = string(hash)
	b, _ := json.MarshalIndent(users, "", "  ")
	write("users.json", b)
	f, e := os.OpenFile(filepath.Join(*out, "credentials.txt"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	must(e)
	_, e = fmt.Fprintf(f, "%s %s\n", *user, password)
	must(e)
	must(f.Close())
	if !*add {
		for _, name := range []string{"cluster-key", "signing-key", "encryption-key"} {
			write(name, []byte(random(32)))
		}
		caKey, e := rsa.GenerateKey(rand.Reader, 3072)
		must(e)
		serial := func() *big.Int {
			n, e := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
			must(e)
			return n
		}
		ca := &x509.Certificate{SerialNumber: serial(), Subject: pkix.Name{CommonName: "DFSha local CA"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().AddDate(3, 0, 0), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature}
		publicDER, e := x509.MarshalPKIXPublicKey(&caKey.PublicKey)
		must(e)
		ski := sha256.Sum256(publicDER)
		ca.SubjectKeyId = ski[:20]
		ca.AuthorityKeyId = ca.SubjectKeyId
		der, e := x509.CreateCertificate(rand.Reader, ca, ca, &caKey.PublicKey, caKey)
		must(e)
		write("ca.crt", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
		write("ca.key", pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(caKey)}))
		key, e := rsa.GenerateKey(rand.Reader, 3072)
		must(e)
		cert := &x509.Certificate{SerialNumber: serial(), Subject: pkix.Name{CommonName: "DFSha cluster"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().AddDate(1, 0, 0), KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth}}
		cert.AuthorityKeyId = ca.SubjectKeyId
		leafDER, e := x509.MarshalPKIXPublicKey(&key.PublicKey)
		must(e)
		leafSKI := sha256.Sum256(leafDER)
		cert.SubjectKeyId = leafSKI[:20]
		for _, h := range strings.Split(*hosts, ",") {
			h = strings.TrimSpace(h)
			if ip := net.ParseIP(h); ip != nil {
				cert.IPAddresses = append(cert.IPAddresses, ip)
			} else if h != "" {
				cert.DNSNames = append(cert.DNSNames, h)
			}
		}
		der, e = x509.CreateCertificate(rand.Reader, cert, ca, &key.PublicKey, caKey)
		must(e)
		write("cluster.crt", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
		write("cluster.key", pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
	}
	fmt.Printf("Configuración privada lista en %s. Credenciales en credentials.txt; no publicar este directorio.\n", *out)
}
