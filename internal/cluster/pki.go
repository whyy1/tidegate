package cluster

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ClusterCA manages the Tidegate cluster Certificate Authority used for mutual TLS
// authentication between the controller and load generator agents.
type ClusterCA struct {
	cert    *x509.Certificate
	key     *ecdsa.PrivateKey
	certPEM []byte
	pool    *x509.CertPool
	fp      string
}

// LoadOrCreateClusterCA loads the ECDSA P-256 Cluster CA from certPath/keyPath or
// generates and persists a new 10-year CA pair when absent.
func LoadOrCreateClusterCA(certPath, keyPath string) (*ClusterCA, error) {
	if certPEM, err := os.ReadFile(certPath); err == nil {
		if keyPEM, kerr := os.ReadFile(keyPath); kerr == nil {
			if ca, perr := parseClusterCA(certPEM, keyPEM); perr == nil {
				return ca, nil
			}
		}
	}

	ca, keyPEM, err := generateClusterCA()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(certPath), 0o700); err != nil {
		return nil, fmt.Errorf("创建集群证书目录失败：%w", err)
	}
	if err := os.WriteFile(certPath, ca.certPEM, 0o644); err != nil {
		return nil, fmt.Errorf("写入集群 CA 证书失败：%w", err)
	}
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		return nil, fmt.Errorf("写入集群 CA 私钥失败：%w", err)
	}
	return ca, nil
}

// NewEphemeralClusterCA creates an in-memory Cluster CA without touching disk.
func NewEphemeralClusterCA() (*ClusterCA, error) {
	ca, _, err := generateClusterCA()
	return ca, err
}

func generateClusterCA() (*ClusterCA, []byte, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("生成集群 CA 密钥失败：%w", err)
	}
	serial, err := randomSerial()
	if err != nil {
		return nil, nil, err
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			Organization: []string{"Tidegate Cluster CA"},
			CommonName:   "Tidegate Cluster Root CA",
		},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(10 * 365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLenZero:        true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, fmt.Errorf("签发集群 CA 证书失败：%w", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, nil, err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, nil, err
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	pool := x509.NewCertPool()
	pool.AddCert(cert)
	return &ClusterCA{
		cert:    cert,
		key:     key,
		certPEM: certPEM,
		pool:    pool,
		fp:      CertFingerprintHex(cert),
	}, keyPEM, nil
}

func parseClusterCA(certPEM, keyPEM []byte) (*ClusterCA, error) {
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil || len(pair.Certificate) == 0 {
		return nil, errors.New("集群 CA 密钥对无效")
	}
	cert, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil || !cert.IsCA {
		return nil, errors.New("集群 CA 证书无效")
	}
	eck, ok := pair.PrivateKey.(*ecdsa.PrivateKey)
	if !ok {
		return nil, errors.New("集群 CA 私钥类型不受支持")
	}
	if time.Now().After(cert.NotAfter) {
		return nil, errors.New("集群 CA 证书已过期")
	}
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	return &ClusterCA{
		cert:    cert,
		key:     eck,
		certPEM: bytes.Clone(certPEM),
		pool:    pool,
		fp:      CertFingerprintHex(cert),
	}, nil
}

// Fingerprint returns the lowercase SHA-256 hex digest of the CA DER bytes.
func (ca *ClusterCA) Fingerprint() string {
	if ca == nil {
		return ""
	}
	return ca.fp
}

// CertPEM returns a defensive copy of the PEM-encoded Cluster CA certificate.
func (ca *ClusterCA) CertPEM() []byte {
	if ca == nil {
		return nil
	}
	return bytes.Clone(ca.certPEM)
}

// IssueServerTLSConfig mints an ephemeral server certificate signed by ca and
// returns a TLS 1.3 config configured for optional-at-handshake / verified mTLS.
func (ca *ClusterCA) IssueServerTLSConfig(hosts []string) (*tls.Config, error) {
	if ca == nil {
		return nil, errors.New("cluster: nil CA")
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("生成集群服务端私钥失败：%w", err)
	}
	serial, err := randomSerial()
	if err != nil {
		return nil, err
	}

	dnsNames := map[string]bool{"localhost": true}
	if hn, err := os.Hostname(); err == nil && hn != "" {
		dnsNames[hn] = true
	}
	ips := map[string]net.IP{
		"127.0.0.1": net.ParseIP("127.0.0.1"),
		"::1":       net.ParseIP("::1"),
	}
	for _, h := range hosts {
		host := strings.TrimSpace(h)
		if host == "" {
			continue
		}
		if nh, _, splitErr := net.SplitHostPort(host); splitErr == nil {
			host = nh
		}
		host = strings.Trim(host, "[]")
		if host == "" || host == "0.0.0.0" || host == "::" {
			continue
		}
		if ip := net.ParseIP(host); ip != nil {
			ips[ip.String()] = ip
		} else {
			dnsNames[host] = true
		}
	}

	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			Organization: []string{"Tidegate Controller"},
			CommonName:   "tidegate-controller",
		},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(2 * 365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	for name := range dnsNames {
		tmpl.DNSNames = append(tmpl.DNSNames, name)
	}
	for _, ip := range ips {
		tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		return nil, fmt.Errorf("签发集群服务端证书失败：%w", err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}

	tlsCert := tls.Certificate{
		Certificate: [][]byte{der, ca.cert.Raw},
		PrivateKey:  key,
		Leaf:        leaf,
	}
	return &tls.Config{
		Certificates: []tls.Certificate{tlsCert},
		ClientAuth:   tls.VerifyClientCertIfGiven,
		ClientCAs:    ca.pool,
		MinVersion:   tls.VersionTLS13,
	}, nil
}

// SignCSR validates csrDER and signs a client authentication certificate for an agent.
func (ca *ClusterCA) SignCSR(csrDER []byte, nodeName string, ttl time.Duration) (certPEM []byte, serialHex string, expiresAt time.Time, err error) {
	if ca == nil {
		return nil, "", time.Time{}, errors.New("cluster: nil CA")
	}
	csr, err := x509.ParseCertificateRequest(csrDER)
	if err != nil {
		return nil, "", time.Time{}, fmt.Errorf("解析节点证书请求失败：%w", err)
	}
	if err := csr.CheckSignature(); err != nil {
		return nil, "", time.Time{}, fmt.Errorf("节点证书请求签名无效：%w", err)
	}
	if ttl <= 0 {
		ttl = 365 * 24 * time.Hour
	}
	serial, err := randomSerial()
	if err != nil {
		return nil, "", time.Time{}, err
	}
	cn := strings.TrimSpace(nodeName)
	if cn == "" {
		cn = strings.TrimSpace(csr.Subject.CommonName)
	}
	if cn == "" {
		cn = "tidegate-agent"
	}
	now := time.Now()
	expiresAt = now.Add(ttl)
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			Organization: []string{"Tidegate Agent"},
			CommonName:   cn,
		},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              expiresAt,
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, csr.PublicKey, ca.key)
	if err != nil {
		return nil, "", time.Time{}, fmt.Errorf("签发节点证书失败：%w", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, "", time.Time{}, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), CertSerialHex(cert), expiresAt, nil
}

// GenerateClientCSR generates a fresh ECDSA P-256 key pair and PKCS#10 CSR for agent enrollment.
func GenerateClientCSR(nodeName string) (csrDER []byte, keyPEM []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("生成节点私钥失败：%w", err)
	}
	cn := strings.TrimSpace(nodeName)
	if cn == "" {
		cn = "tidegate-agent"
	}
	csrDER, err = x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject: pkix.Name{
			Organization: []string{"Tidegate Agent"},
			CommonName:   cn,
		},
	}, key)
	if err != nil {
		return nil, nil, fmt.Errorf("生成节点 CSR 失败：%w", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, nil, err
	}
	return csrDER, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), nil
}

// ClientTLSConfig constructs a mutual-TLS client config verifying the server leaf
// against caCertPEM and checking expectedCAFingerprint when non-empty.
func ClientTLSConfig(caCertPEM, clientCertPEM, clientKeyPEM []byte, expectedCAFingerprint string) (*tls.Config, error) {
	block, _ := pem.Decode(caCertPEM)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, errors.New("集群 CA 证书 PEM 格式无效")
	}
	caCert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("解析集群 CA 证书失败：%w", err)
	}
	wantFP, err := validateFingerprintInput(expectedCAFingerprint)
	if err != nil {
		return nil, err
	}
	gotFP := CertFingerprintHex(caCert)
	if wantFP != "" && gotFP != wantFP {
		return nil, fmt.Errorf("控制器 CA 指纹不匹配（期望 %s，实际 %s）", wantFP, gotFP)
	}

	pair, err := tls.X509KeyPair(clientCertPEM, clientKeyPEM)
	if err != nil {
		return nil, fmt.Errorf("解析节点客户端证书失败：%w", err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(caCert)

	return &tls.Config{
		Certificates:       []tls.Certificate{pair},
		MinVersion:         tls.VersionTLS13,
		InsecureSkipVerify: true, // Verified via VerifyPeerCertificate against pinned Cluster CA without DNS name restrictions.
		VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
			if len(rawCerts) == 0 {
				return errors.New("控制器未提供 TLS 证书")
			}
			leaf, err := x509.ParseCertificate(rawCerts[0])
			if err != nil {
				return err
			}
			_, err = leaf.Verify(x509.VerifyOptions{
				Roots:     pool,
				KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
			})
			return err
		},
	}, nil
}

// BootstrapTLSConfig creates a TLS config for the initial Enroll RPC, enforcing
// expectedCAFingerprint when specified.
func BootstrapTLSConfig(expectedCAFingerprint string) (*tls.Config, error) {
	wantFP, err := validateFingerprintInput(expectedCAFingerprint)
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		MinVersion:         tls.VersionTLS13,
		InsecureSkipVerify: true, // Enrollment either pins expectedCAFingerprint or performs TOFU for initial CA download.
		VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
			if len(rawCerts) == 0 {
				return errors.New("控制器未提供 TLS 证书")
			}
			if wantFP == "" {
				return nil
			}
			var certs []*x509.Certificate
			var matchedCA *x509.Certificate
			for _, raw := range rawCerts {
				c, err := x509.ParseCertificate(raw)
				if err != nil {
					continue
				}
				certs = append(certs, c)
				if CertFingerprintHex(c) == wantFP {
					matchedCA = c
				}
			}
			if matchedCA == nil || len(certs) == 0 {
				return fmt.Errorf("控制器 CA 指纹与指定值 %s 不匹配", wantFP)
			}
			pool := x509.NewCertPool()
			pool.AddCert(matchedCA)
			_, err := certs[0].Verify(x509.VerifyOptions{
				Roots:     pool,
				KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
			})
			return err
		},
	}, nil
}

// CertFingerprintHex returns the lowercase SHA-256 fingerprint of cert.
func CertFingerprintHex(cert *x509.Certificate) string {
	if cert == nil {
		return ""
	}
	sum := sha256.Sum256(cert.Raw)
	return hex.EncodeToString(sum[:])
}

// CertSerialHex returns the lowercase hexadecimal serial number of cert.
func CertSerialHex(cert *x509.Certificate) string {
	if cert == nil || cert.SerialNumber == nil {
		return ""
	}
	return strings.ToLower(cert.SerialNumber.Text(16))
}

// NormalizeFingerprint strips common separators (: - space) and lowercases hex digits.
func NormalizeFingerprint(s string) string {
	return strings.ToLower(strings.NewReplacer(":", "", "-", "", " ", "", "\t", "").Replace(s))
}

func validateFingerprintInput(s string) (string, error) {
	norm := NormalizeFingerprint(s)
	if norm == "" {
		return "", nil
	}
	if len(norm) != 64 {
		return "", errors.New("CA SHA-256 指纹长度无效，应为 64 位十六进制字符")
	}
	if _, err := hex.DecodeString(norm); err != nil {
		return "", errors.New("CA SHA-256 指纹包含非法十六进制字符")
	}
	return norm, nil
}

func randomSerial() (*big.Int, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return nil, err
	}
	b[0] &= 0x7f
	if b[0] == 0 {
		b[0] = 0x01
	}
	return new(big.Int).SetBytes(b[:]), nil
}
