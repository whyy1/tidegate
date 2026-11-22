package auth

import (
	"net/http"
	"time"
)

// CookieName is the session cookie name.
const CookieName = "tidegate_session"

// NewSessionCookie returns the session cookie: HttpOnly, SameSite=Strict,
// Path=/, expiring with the session, and Secure when secure is true (set it
// when the request arrived over TLS).
func NewSessionCookie(token string, expires time.Time, secure bool) *http.Cookie {
	return &http.Cookie{
		Name:     CookieName,
		Value:    token,
		Path:     "/",
		Expires:  expires.UTC(),
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteStrictMode,
	}
}

// ExpiredSessionCookie returns a cookie that removes the session cookie from
// the browser.
func ExpiredSessionCookie(secure bool) *http.Cookie {
	return &http.Cookie{
		Name:     CookieName,
		Value:    "",
		Path:     "/",
		Expires:  time.Unix(0, 0).UTC(),
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteStrictMode,
	}
}

// SetSessionCookie writes the cookie for a login or setup result.
func SetSessionCookie(w http.ResponseWriter, res *LoginResult, secure bool) {
	http.SetCookie(w, NewSessionCookie(res.Token, res.Session.ExpiresAt, secure))
}

// ClearSessionCookie writes a cookie that removes the session cookie.
func ClearSessionCookie(w http.ResponseWriter, secure bool) {
	http.SetCookie(w, ExpiredSessionCookie(secure))
}

// SessionToken returns the session token sent by the client, or "".
func SessionToken(r *http.Request) string {
	c, err := r.Cookie(CookieName)
	if err != nil {
		return ""
	}
	return c.Value
}
