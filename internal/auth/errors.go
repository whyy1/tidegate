package auth

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

// Code identifies the kind of an authentication error. Codes are stable
// strings meant to be sent to API clients.
type Code string

// Error codes returned by this package.
const (
	CodeInvalidCredentials Code = "invalid_credentials"
	CodeRateLimited        Code = "rate_limited"
	CodeSetupRequired      Code = "setup_required"
	CodeSetupDone          Code = "setup_done"
	CodeInvalidSetupCode   Code = "invalid_setup_code"
	CodeSetupCodeExpired   Code = "setup_code_expired"
	CodeInvalidUsername    Code = "invalid_username"
	CodeWeakPassword       Code = "weak_password"
	CodeIncorrectPassword  Code = "incorrect_password"
	CodeUnauthenticated    Code = "unauthenticated"
	CodeSessionExpired     Code = "session_expired"
	CodeSessionNotFound    Code = "session_not_found"
	CodeUserNotFound       Code = "user_not_found"
	CodeCSRFHeaderMissing  Code = "csrf_header_missing"
	CodeOriginNotAllowed   Code = "origin_not_allowed"
	CodeInsecureTransport  Code = "insecure_transport"
	CodeForbidden          Code = "forbidden"
	CodeTokenExpired       Code = "token_expired"
	CodeTokenNotFound      Code = "token_not_found"
	CodeUserExists         Code = "user_exists"
	CodeLastAdmin          Code = "last_admin"
	CodeInvalidRole        Code = "invalid_role"
	CodeInvalidTokenInput  Code = "invalid_token_input"
	CodeTOTPRequired       Code = "totp_required"
	CodeInvalidTOTP        Code = "invalid_totp"
	CodeTOTPAlreadyEnabled Code = "totp_already_enabled"
	CodeTOTPNotEnabled     Code = "totp_not_enabled"
	CodeScopeForbidden     Code = "insufficient_scope"
	CodeInternal           Code = "internal_error"
)

// Error is the typed error returned by this package. Message is written in
// Simplified Chinese and is safe to show to end users; it never contains
// secrets. Status is the suggested HTTP status code.
type Error struct {
	Code       Code
	Message    string
	Status     int
	RetryAfter time.Duration // only set for CodeRateLimited
}

func (e *Error) Error() string { return string(e.Code) + ": " + e.Message }

// Is reports whether target is an *Error with the same Code, so that
// errors.Is(err, ErrWeakPassword) matches every weak password variant.
func (e *Error) Is(target error) bool {
	t, ok := target.(*Error)
	return ok && t.Code == e.Code
}

// Sentinel errors. Compare with errors.Is; the concrete error may carry a
// more specific message or a RetryAfter value.
var (
	ErrInvalidCredentials = &Error{Code: CodeInvalidCredentials, Status: http.StatusUnauthorized, Message: "用户名或密码错误"}
	ErrRateLimited        = &Error{Code: CodeRateLimited, Status: http.StatusTooManyRequests, Message: "尝试次数过多，请稍后再试"}
	ErrSetupRequired      = &Error{Code: CodeSetupRequired, Status: http.StatusConflict, Message: "系统尚未初始化，请先创建管理员账户"}
	ErrSetupDone          = &Error{Code: CodeSetupDone, Status: http.StatusConflict, Message: "系统已完成初始化"}
	ErrInvalidSetupCode   = &Error{Code: CodeInvalidSetupCode, Status: http.StatusForbidden, Message: "初始化码不正确"}
	ErrSetupCodeExpired   = &Error{Code: CodeSetupCodeExpired, Status: http.StatusForbidden, Message: "初始化码已过期，请重启服务获取新的初始化码"}
	ErrInvalidUsername    = &Error{Code: CodeInvalidUsername, Status: http.StatusBadRequest, Message: "用户名不符合要求"}
	ErrWeakPassword       = &Error{Code: CodeWeakPassword, Status: http.StatusBadRequest, Message: "密码不符合要求"}
	ErrIncorrectPassword  = &Error{Code: CodeIncorrectPassword, Status: http.StatusBadRequest, Message: "当前密码不正确"}
	ErrUnauthenticated    = &Error{Code: CodeUnauthenticated, Status: http.StatusUnauthorized, Message: "请先登录"}
	ErrSessionExpired     = &Error{Code: CodeSessionExpired, Status: http.StatusUnauthorized, Message: "登录已过期，请重新登录"}
	ErrSessionNotFound    = &Error{Code: CodeSessionNotFound, Status: http.StatusNotFound, Message: "会话不存在或已失效"}
	ErrUserNotFound       = &Error{Code: CodeUserNotFound, Status: http.StatusNotFound, Message: "用户不存在"}
	ErrCSRFHeaderMissing  = &Error{Code: CodeCSRFHeaderMissing, Status: http.StatusForbidden, Message: "请求缺少 " + RequestHeader + " 请求头"}
	ErrOriginNotAllowed   = &Error{Code: CodeOriginNotAllowed, Status: http.StatusForbidden, Message: "请求来源不受信任"}
	ErrInsecureTransport = &Error{Code: CodeInsecureTransport, Status: http.StatusForbidden, Message: "当前连接未加密，为保护密码只允许从本机登录。请启用 TLS，或用 --allow-insecure-remote 启动"}
	ErrForbidden         = &Error{Code: CodeForbidden, Status: http.StatusForbidden, Message: "无权执行此操作"}
	ErrTokenExpired      = &Error{Code: CodeTokenExpired, Status: http.StatusUnauthorized, Message: "访问令牌已过期"}
	ErrTokenNotFound     = &Error{Code: CodeTokenNotFound, Status: http.StatusNotFound, Message: "访问令牌不存在"}
	ErrUserExists        = &Error{Code: CodeUserExists, Status: http.StatusConflict, Message: "用户名已存在"}
	ErrLastAdmin         = &Error{Code: CodeLastAdmin, Status: http.StatusConflict, Message: "不能删除或降级最后一个管理员账户"}
	ErrInvalidRole        = &Error{Code: CodeInvalidRole, Status: http.StatusBadRequest, Message: "角色只能是 admin、member 或 viewer"}
	ErrInvalidTokenInput  = &Error{Code: CodeInvalidTokenInput, Status: http.StatusBadRequest, Message: "访问令牌名称不能为空且不能超过 100 个字符"}
	ErrTOTPRequired       = &Error{Code: CodeTOTPRequired, Status: http.StatusUnauthorized, Message: "该账号已开启双因素认证，请输入 6 位动态验证码或恢复码"}
	ErrInvalidTOTP        = &Error{Code: CodeInvalidTOTP, Status: http.StatusUnauthorized, Message: "动态验证码或恢复码无效"}
	ErrTOTPAlreadyEnabled = &Error{Code: CodeTOTPAlreadyEnabled, Status: http.StatusConflict, Message: "当前账号已启用双因素认证"}
	ErrTOTPNotEnabled     = &Error{Code: CodeTOTPNotEnabled, Status: http.StatusBadRequest, Message: "当前账号尚未启用或未完成双因素认证初始化"}
	ErrScopeForbidden     = &Error{Code: CodeScopeForbidden, Status: http.StatusForbidden, Message: "当前访问令牌缺少执行该操作所需的 Scope 权限范围"}

	errInternal = &Error{Code: CodeInternal, Status: http.StatusInternalServerError, Message: "服务器内部错误"}
)

func weakPassword(msg string) *Error {
	return &Error{Code: CodeWeakPassword, Status: http.StatusBadRequest, Message: msg}
}

func invalidUsername(msg string) *Error {
	return &Error{Code: CodeInvalidUsername, Status: http.StatusBadRequest, Message: msg}
}

func rateLimited(wait time.Duration) *Error {
	return &Error{
		Code:       CodeRateLimited,
		Status:     http.StatusTooManyRequests,
		Message:    "尝试次数过多，请" + formatWait(wait) + "后再试",
		RetryAfter: wait,
	}
}

// formatWait renders a wait time for users, rounding up.
func formatWait(d time.Duration) string {
	secs := ceilSeconds(d)
	if secs >= 120 {
		return fmt.Sprintf(" %d 分钟", (secs+59)/60)
	}
	return fmt.Sprintf(" %d 秒", secs)
}

func ceilSeconds(d time.Duration) int {
	if d <= 0 {
		return 0
	}
	return int((d + time.Second - 1) / time.Second)
}

// AsError returns the *Error in err's chain, if any.
func AsError(err error) (*Error, bool) {
	var e *Error
	if errors.As(err, &e) {
		return e, true
	}
	return nil, false
}

// RetryAfter returns how long the caller should wait when err is a rate
// limit error.
func RetryAfter(err error) (time.Duration, bool) {
	if e, ok := AsError(err); ok && e.Code == CodeRateLimited {
		return e.RetryAfter, true
	}
	return 0, false
}

// HTTPStatus returns the suggested HTTP status for err. Errors that are not
// an *Error map to 500.
func HTTPStatus(err error) int {
	if e, ok := AsError(err); ok && e.Status != 0 {
		return e.Status
	}
	return http.StatusInternalServerError
}

type errorBody struct {
	Error errorDetail `json:"error"`
}

type errorDetail struct {
	Code    Code   `json:"code"`
	Message string `json:"message"`
}

// WriteError writes err as {"error":{"code":"...","message":"..."}} with the
// suggested status. Errors that are not an *Error are reported as a generic
// internal error so that internal details never reach the client. A
// Retry-After header is added for rate limit errors.
func WriteError(w http.ResponseWriter, err error) {
	e, ok := AsError(err)
	if !ok {
		e = errInternal
	}
	status := e.Status
	if status == 0 {
		status = http.StatusInternalServerError
	}
	h := w.Header()
	h.Set("Content-Type", "application/json; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	if e.RetryAfter > 0 {
		h.Set("Retry-After", strconv.Itoa(ceilSeconds(e.RetryAfter)))
	}
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(errorBody{Error: errorDetail{Code: e.Code, Message: e.Message}})
}
