package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"fmt"
	"math/big"
	"net"
	"strings"
	"time"
)

func loadTLSConfig(certFile, keyFile string) (*tls.Config, error) {
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("加载 TLS 证书失败：%w", err)
	}
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
	}, nil
}

func selfSignedTLSConfig(hosts []string) (*tls.Config, string, error) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, "", fmt.Errorf("生成自签名证书密钥失败：%w", err)
	}
	serialLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serial, err := rand.Int(rand.Reader, serialLimit)
	if err != nil {
		return nil, "", fmt.Errorf("生成证书序列号失败：%w", err)
	}

	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName:   "Tidegate",
			Organization: []string{"Tidegate Self-Signed"},
		},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{"localhost"},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
	}

	seenDNS := map[string]bool{"localhost": true}
	seenIP := map[string]bool{"127.0.0.1": true, "::1": true}
	for _, raw := range hosts {
		h := strings.TrimSpace(strings.Trim(raw, "[]"))
		if h == "" || h == "0.0.0.0" || h == "::" {
			continue
		}
		if ip := net.ParseIP(h); ip != nil {
			key := ip.String()
			if !seenIP[key] {
				seenIP[key] = true
				tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
			}
		} else {
			lower := strings.ToLower(h)
			if !seenDNS[lower] {
				seenDNS[lower] = true
				tmpl.DNSNames = append(tmpl.DNSNames, lower)
			}
		}
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		return nil, "", fmt.Errorf("签发自签名证书失败：%w", err)
	}
	cert := tls.Certificate{
		Certificate: [][]byte{der},
		PrivateKey:  priv,
	}
	sum := sha256.Sum256(der)
	fp := formatFingerprint(sum[:])

	cfg := &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
	}
	return cfg, fp, nil
}

func formatFingerprint(b []byte) string {
	var out strings.Builder
	for i, v := range b {
		if i > 0 {
			out.WriteByte(':')
		}
		var buf [2]byte
		hex.Encode(buf[:], []byte{v})
		out.WriteString(strings.ToUpper(string(buf[:])))
	}
	return out.String()
}
