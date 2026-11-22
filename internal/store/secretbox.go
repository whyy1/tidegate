package store

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// SecretMask replaces decrypted secret variable values in UI and read responses.
const SecretMask = "••••••••"

const (
	secretMasterKeyBytes   = 32
	secretEnvelopeVersion  = byte(0x01)
	secretMasterKeyPerm    = 0o600
)

// SecretBox encrypts and decrypts secret variable values using AES-256-GCM
// with domain-separated additional authenticated data (projectID, scope, name).
type SecretBox struct {
	aead cipher.AEAD
}

// NewSecretBox constructs a SecretBox from a 32-byte AES-256 key.
func NewSecretBox(key []byte) (*SecretBox, error) {
	if len(key) != secretMasterKeyBytes {
		return nil, fmt.Errorf("主密钥长度无效：需要 %d 字节，实际 %d 字节", secretMasterKeyBytes, len(key))
	}
	blk, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(blk)
	if err != nil {
		return nil, err
	}
	return &SecretBox{aead: aead}, nil
}

// LoadOrCreateMasterKey reads a 32-byte AES-256 master key from path (accepting
// 64 hex chars, base64, or raw 32 bytes), or creates a new 64-hex key file with
// 0600 permissions when path does not exist.
func LoadOrCreateMasterKey(path string) (*SecretBox, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, errors.New("主密钥文件路径不能为空")
	}
	raw, err := os.ReadFile(path)
	if err == nil {
		key, err := decodeMasterKey(raw)
		if err != nil {
			return nil, fmt.Errorf("解析主密钥文件 %s 失败：%w", path, err)
		}
		return NewSecretBox(key)
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("读取主密钥文件 %s 失败：%w", path, err)
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("创建主密钥目录失败：%w", err)
	}
	key := make([]byte, secretMasterKeyBytes)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("生成主密钥失败：%w", err)
	}
	encoded := []byte(hex.EncodeToString(key) + "\n")
	tmp, err := os.CreateTemp(filepath.Dir(path), ".master.key.tmp-*")
	if err != nil {
		return nil, fmt.Errorf("创建主密钥临时文件失败：%w", err)
	}
	tmpName := tmp.Name()
	_, werr := tmp.Write(encoded)
	_ = tmp.Chmod(secretMasterKeyPerm)
	cerr := tmp.Close()
	if werr != nil || cerr != nil {
		_ = os.Remove(tmpName)
		if werr != nil {
			return nil, fmt.Errorf("写入主密钥失败：%w", werr)
		}
		return nil, fmt.Errorf("关闭主密钥文件失败：%w", cerr)
	}
	if err := renameRetry(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		// Another starter may have won the race.
		if existing, rerr := os.ReadFile(path); rerr == nil {
			if k, derr := decodeMasterKey(existing); derr == nil {
				return NewSecretBox(k)
			}
		}
		return nil, fmt.Errorf("保存主密钥文件失败：%w", err)
	}
	_ = os.Chmod(path, secretMasterKeyPerm)
	return NewSecretBox(key)
}

func decodeMasterKey(raw []byte) ([]byte, error) {
	trimmed := strings.TrimSpace(string(bytes.TrimPrefix(raw, utf8BOM)))
	if len(trimmed) == hex.EncodedLen(secretMasterKeyBytes) {
		if k, err := hex.DecodeString(trimmed); err == nil && len(k) == secretMasterKeyBytes {
			return k, nil
		}
	}
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if k, err := enc.DecodeString(trimmed); err == nil && len(k) == secretMasterKeyBytes {
			return k, nil
		}
	}
	if len(raw) == secretMasterKeyBytes {
		return append([]byte(nil), raw...), nil
	}
	return nil, errors.New("无法识别的 256-bit 密钥编码")
}

func secretAAD(projectID, scope, name string) []byte {
	return []byte(projectID + "\x00" + scope + "\x00" + name)
}

// Seal encrypts plaintext bound to (projectID, scope, name).
func (b *SecretBox) Seal(projectID, scope, name, plaintext string) ([]byte, error) {
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	out := make([]byte, 1+len(nonce), 1+len(nonce)+len(plaintext)+b.aead.Overhead())
	out[0] = secretEnvelopeVersion
	copy(out[1:], nonce)
	return b.aead.Seal(out, nonce, []byte(plaintext), secretAAD(projectID, scope, name)), nil
}

// Open verifies AAD and decrypts ciphertext produced by Seal.
func (b *SecretBox) Open(projectID, scope, name string, ciphertext []byte) (string, error) {
	ns := b.aead.NonceSize()
	if len(ciphertext) < 1+ns+b.aead.Overhead() || ciphertext[0] != secretEnvelopeVersion {
		return "", errors.New("密文格式无效或版本不受支持")
	}
	plain, err := b.aead.Open(nil, ciphertext[1:1+ns], ciphertext[1+ns:], secretAAD(projectID, scope, name))
	if err != nil {
		return "", fmt.Errorf("敏感变量解密失败：%w", err)
	}
	return string(plain), nil
}

// SecretVault backs Store project and environment secret variables in ResultDB.
type SecretVault struct {
	rdb *ResultDB
	box *SecretBox
}

// NewSecretVault constructs a SecretVault pairing rdb and box.
func NewSecretVault(rdb *ResultDB, box *SecretBox) *SecretVault {
	if rdb == nil || box == nil {
		return nil
	}
	return &SecretVault{rdb: rdb, box: box}
}

// SyncVariables encrypts changed Secret:true variables into SQLite, keeps
// existing ciphertexts when callers send SecretMask or empty values, removes
// orphaned secrets for that scope, and returns (onDisk, maskedView).
func (v *SecretVault) SyncVariables(ctx context.Context, projectID, scope string, vars KeyValueList) (onDisk KeyValueList, masked KeyValueList, err error) {
	if len(vars) == 0 {
		_ = v.rdb.DeleteScopeProjectSecrets(ctx, projectID, scope)
		return nil, nil, nil
	}
	existing, err := v.rdb.ProjectSecrets(ctx, projectID, scope)
	if err != nil {
		return nil, nil, err
	}

	onDisk = make(KeyValueList, len(vars))
	masked = make(KeyValueList, len(vars))
	wantNames := make(map[string]bool)

	for i, kv := range vars {
		onDisk[i] = kv
		masked[i] = kv
		name := strings.TrimSpace(kv.Name)
		if !kv.Secret || name == "" {
			continue
		}
		wantNames[name] = true
		val := string(kv.Value)
		if val != "" && val != SecretMask {
			blob, err := v.box.Seal(projectID, scope, name, val)
			if err != nil {
				return nil, nil, err
			}
			if err := v.rdb.SetProjectSecret(ctx, projectID, scope, name, blob); err != nil {
				return nil, nil, err
			}
			onDisk[i].Value = ""
			masked[i].Value = SecretMask
			continue
		}
		onDisk[i].Value = ""
		if _, exists := existing[name]; exists {
			masked[i].Value = SecretMask
		} else {
			masked[i].Value = ""
		}
	}

	for name := range existing {
		if !wantNames[name] {
			if err := v.rdb.DeleteProjectSecret(ctx, projectID, scope, name); err != nil {
				return nil, nil, err
			}
		}
	}
	return onDisk, masked, nil
}

// MaskVariables masks secret entries in vars as SecretMask without exposing ciphertext.
func (v *SecretVault) MaskVariables(ctx context.Context, projectID, scope string, vars KeyValueList) KeyValueList {
	if len(vars) == 0 {
		return vars
	}
	sec, err := v.rdb.ProjectSecrets(ctx, projectID, scope)
	if err != nil {
		sec = nil
	}
	out := make(KeyValueList, len(vars))
	for i, kv := range vars {
		out[i] = kv
		if !kv.Secret {
			continue
		}
		name := strings.TrimSpace(kv.Name)
		if _, ok := sec[name]; ok || string(kv.Value) != "" {
			out[i].Value = SecretMask
		}
	}
	return out
}

// ResolveVariables decrypts stored secrets for (projectID, scope) into name->plaintext.
func (v *SecretVault) ResolveVariables(ctx context.Context, projectID, scope string) (map[string]string, error) {
	sec, err := v.rdb.ProjectSecrets(ctx, projectID, scope)
	if err != nil || len(sec) == 0 {
		return nil, err
	}
	out := make(map[string]string, len(sec))
	for name, blob := range sec {
		plain, err := v.box.Open(projectID, scope, name, blob)
		if err != nil {
			return nil, fmt.Errorf("解密敏感变量 %q 失败：%w", name, err)
		}
		out[name] = plain
	}
	return out, nil
}

// DeleteScope removes all secrets under (projectID, scope).
func (v *SecretVault) DeleteScope(ctx context.Context, projectID, scope string) error {
	return v.rdb.DeleteScopeProjectSecrets(ctx, projectID, scope)
}

// DeleteProject removes all secrets owned by projectID.
func (v *SecretVault) DeleteProject(ctx context.Context, projectID string) error {
	return v.rdb.DeleteAllProjectSecrets(ctx, projectID)
}
