package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base32"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

const (
	totpPeriodSeconds = 30
	totpDigits        = 6
	totpSkewSteps     = 1
	totpIssuer        = "Tidegate"
	recoveryCodeCount = 8
)

var totpEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// TOTPEnrollment holds the temporary secret and otpauth provisioning URI shown
// to the user during 2FA activation.
type TOTPEnrollment struct {
	Secret     string `json:"secret"`
	OTPAuthURL string `json:"otpauth_url"`
}

func computeTOTPCode(secret []byte, step int64) string {
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], uint64(step))
	mac := hmac.New(sha1.New, secret)
	mac.Write(msg[:])
	sum := mac.Sum(nil)
	offset := sum[len(sum)-1] & 0x0f
	bin := (int(sum[offset]&0x7f) << 24) |
		(int(sum[offset+1]&0xff) << 16) |
		(int(sum[offset+2]&0xff) << 8) |
		int(sum[offset+3]&0xff)
	return fmt.Sprintf("%06d", bin%1_000_000)
}

func normalizeTOTPInput(raw string) string {
	s := strings.TrimSpace(raw)
	s = strings.ReplaceAll(s, " ", "")
	s = strings.ReplaceAll(s, "-", "")
	return strings.ToUpper(s)
}

func matchTOTPStep(secretB32, code string, now time.Time) (int64, bool) {
	norm := normalizeTOTPInput(code)
	if len(norm) != totpDigits {
		return 0, false
	}
	for _, r := range norm {
		if r < '0' || r > '9' {
			return 0, false
		}
	}
	cleanSec := strings.TrimRight(strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(secretB32), " ", "")), "=")
	secret, err := totpEncoding.DecodeString(cleanSec)
	if err != nil || len(secret) == 0 {
		return 0, false
	}
	curStep := now.Unix() / totpPeriodSeconds
	for offset := int64(-totpSkewSteps); offset <= int64(totpSkewSteps); offset++ {
		step := curStep + offset
		if step < 0 {
			continue
		}
		want := computeTOTPCode(secret, step)
		if subtle.ConstantTimeCompare([]byte(norm), []byte(want)) == 1 {
			return step, true
		}
	}
	return 0, false
}

func hashRecoveryCode(normalized string) string {
	sum := sha256.Sum256([]byte("tg-recovery:" + normalized))
	return hex.EncodeToString(sum[:])
}

func generateRecoveryCodes() ([]string, string, error) {
	plain := make([]string, 0, recoveryCodeCount)
	hashes := make([]string, 0, recoveryCodeCount)
	buf := make([]byte, 4)
	for i := 0; i < recoveryCodeCount; i++ {
		if _, err := rand.Read(buf); err != nil {
			return nil, "", fmt.Errorf("auth: generate recovery code: %w", err)
		}
		rawHex := strings.ToUpper(hex.EncodeToString(buf))
		code := rawHex[:4] + "-" + rawHex[4:]
		plain = append(plain, code)
		hashes = append(hashes, hashRecoveryCode(normalizeTOTPInput(code)))
	}
	return plain, strings.Join(hashes, ","), nil
}

// GenerateTOTPCode computes the current 6-digit RFC 6238 code for testing/CLI helpers.
func GenerateTOTPCode(secretB32 string, at time.Time) (string, error) {
	cleanSec := strings.TrimRight(strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(secretB32), " ", "")), "=")
	secret, err := totpEncoding.DecodeString(cleanSec)
	if err != nil || len(secret) == 0 {
		return "", errors.New("auth: invalid TOTP base32 secret")
	}
	return computeTOTPCode(secret, at.Unix()/totpPeriodSeconds), nil
}

// BeginTOTPEnrollment generates a pending Base32 secret and otpauth URI for userID.
func (s *Service) BeginTOTPEnrollment(ctx context.Context, userID int64) (*TOTPEnrollment, error) {
	var (
		username string
		enabled  int
	)
	err := s.db.QueryRowContext(ctx, `SELECT username, totp_enabled FROM auth_users WHERE id = ?`, userID).Scan(&username, &enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrUserNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("auth: begin totp: %w", err)
	}
	if enabled != 0 {
		return nil, ErrTOTPAlreadyEnabled
	}

	raw := make([]byte, 20)
	if _, err := rand.Read(raw); err != nil {
		return nil, fmt.Errorf("auth: generate totp secret: %w", err)
	}
	secret := totpEncoding.EncodeToString(raw)
	if _, err := s.db.ExecContext(ctx,
		`UPDATE auth_users SET totp_pending_secret = ?, updated_at = ? WHERE id = ? AND totp_enabled = 0`,
		secret, s.clock().UnixMilli(), userID); err != nil {
		return nil, fmt.Errorf("auth: store pending totp: %w", err)
	}

	label := url.PathEscape(totpIssuer + ":" + username)
	otpURI := fmt.Sprintf("otpauth://totp/%s?secret=%s&issuer=%s&algorithm=SHA1&digits=%d&period=%d",
		label, url.QueryEscape(secret), url.QueryEscape(totpIssuer), totpDigits, totpPeriodSeconds)

	return &TOTPEnrollment{
		Secret:     secret,
		OTPAuthURL: otpURI,
	}, nil
}

// ConfirmTOTPEnrollment verifies code against the pending TOTP secret, enables 2FA,
// and returns 8 single-use recovery codes.
func (s *Service) ConfirmTOTPEnrollment(ctx context.Context, userID int64, code string) ([]string, error) {
	var (
		pending string
		enabled int
	)
	err := s.db.QueryRowContext(ctx, `SELECT totp_pending_secret, totp_enabled FROM auth_users WHERE id = ?`, userID).Scan(&pending, &enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrUserNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("auth: confirm totp: %w", err)
	}
	if enabled != 0 {
		return nil, ErrTOTPAlreadyEnabled
	}
	if strings.TrimSpace(pending) == "" {
		return nil, ErrTOTPNotEnabled
	}

	now := s.clock()
	step, ok := matchTOTPStep(pending, code, now)
	if !ok {
		return nil, ErrInvalidTOTP
	}
	plainCodes, hashesCSV, err := generateRecoveryCodes()
	if err != nil {
		return nil, err
	}

	res, err := s.db.ExecContext(ctx,
		`UPDATE auth_users
		    SET totp_secret = ?, totp_enabled = 1, totp_last_step = ?, totp_recovery_hashes = ?, totp_pending_secret = '', updated_at = ?
		  WHERE id = ? AND totp_enabled = 0 AND totp_pending_secret = ?`,
		pending, step, hashesCSV, now.UnixMilli(), userID, pending)
	if err != nil {
		return nil, fmt.Errorf("auth: enable totp: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil || n == 0 {
		return nil, ErrTOTPAlreadyEnabled
	}
	return plainCodes, nil
}

func (s *Service) verifyUserTOTP(ctx context.Context, userID int64, code string) error {
	var (
		secret, recCSV string
		enabled        int
		lastStep       int64
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT totp_secret, totp_enabled, totp_last_step, totp_recovery_hashes FROM auth_users WHERE id = ?`, userID).
		Scan(&secret, &enabled, &lastStep, &recCSV)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrUserNotFound
	}
	if err != nil {
		return fmt.Errorf("auth: load totp state: %w", err)
	}
	if enabled == 0 {
		return ErrTOTPNotEnabled
	}

	now := s.clock()
	if step, ok := matchTOTPStep(secret, code, now); ok {
		res, err := s.db.ExecContext(ctx,
			`UPDATE auth_users SET totp_last_step = ?, updated_at = ? WHERE id = ? AND totp_last_step < ?`,
			step, now.UnixMilli(), userID, step)
		if err != nil {
			return fmt.Errorf("auth: update totp step: %w", err)
		}
		if n, _ := res.RowsAffected(); n == 1 {
			return nil
		}
		return ErrInvalidTOTP
	}

	// Fallback: check one-time recovery code.
	norm := normalizeTOTPInput(code)
	if len(norm) == 8 && recCSV != "" {
		wantHash := hashRecoveryCode(norm)
		parts := strings.Split(recCSV, ",")
		matchIdx := -1
		for i, h := range parts {
			if len(h) == len(wantHash) && subtle.ConstantTimeCompare([]byte(h), []byte(wantHash)) == 1 && matchIdx == -1 {
				matchIdx = i
			}
		}
		if matchIdx >= 0 {
			remaining := make([]string, 0, len(parts)-1)
			for i, h := range parts {
				if i != matchIdx && h != "" {
					remaining = append(remaining, h)
				}
			}
			newCSV := strings.Join(remaining, ",")
			res, err := s.db.ExecContext(ctx,
				`UPDATE auth_users SET totp_recovery_hashes = ?, updated_at = ? WHERE id = ? AND totp_recovery_hashes = ?`,
				newCSV, now.UnixMilli(), userID, recCSV)
			if err != nil {
				return fmt.Errorf("auth: consume recovery code: %w", err)
			}
			if n, _ := res.RowsAffected(); n == 1 {
				return nil
			}
		}
	}
	return ErrInvalidTOTP
}

// DisableTOTP disables 2FA for userID after checking current password and a valid TOTP/recovery code.
func (s *Service) DisableTOTP(ctx context.Context, userID int64, currentPassword, code, remoteAddr string) error {
	u, stored, err := s.userByID(ctx, userID)
	if err != nil {
		return err
	}
	if !u.TOTPEnabled {
		return ErrTOTPNotEnabled
	}
	userKey := usernameKey(u.Username)
	addrKey := addressKey(remoteAddr)
	if d := s.loginRetryAfter(userKey, addrKey); d > 0 {
		return rateLimited(d)
	}
	if err := s.acquireHash(ctx); err != nil {
		return err
	}
	defer s.releaseHash()

	ok, err := verifyPassword(currentPassword, stored)
	if err != nil {
		return fmt.Errorf("auth: disable totp password: %w", err)
	}
	if !ok {
		s.loginFailed(userKey, addrKey)
		return ErrIncorrectPassword
	}
	if err := s.verifyUserTOTP(ctx, userID, code); err != nil {
		s.loginFailed(userKey, addrKey)
		return err
	}
	s.loginByUser.reset(userKey)
	s.loginByAddr.reset(addrKey)

	_, err = s.db.ExecContext(ctx,
		`UPDATE auth_users
		    SET totp_secret = '', totp_enabled = 0, totp_last_step = 0, totp_recovery_hashes = '', totp_pending_secret = '', updated_at = ?
		  WHERE id = ?`, s.clock().UnixMilli(), userID)
	if err != nil {
		return fmt.Errorf("auth: disable totp: %w", err)
	}
	return nil
}

// AdminResetTOTP clears all TOTP 2FA state for targetUserID.
func (s *Service) AdminResetTOTP(ctx context.Context, targetUserID int64) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE auth_users
		    SET totp_secret = '', totp_enabled = 0, totp_last_step = 0, totp_recovery_hashes = '', totp_pending_secret = '', updated_at = ?
		  WHERE id = ?`, s.clock().UnixMilli(), targetUserID)
	if err != nil {
		return fmt.Errorf("auth: admin reset totp: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("auth: admin reset totp: %w", err)
	}
	if n == 0 {
		return ErrUserNotFound
	}
	return nil
}
