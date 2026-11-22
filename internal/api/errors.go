// Package api implements the HTTP REST API, static UI fallback, and pure-Go
// RFC 6455 WebSocket push server for Tidegate.
package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"tidegate/internal/auth"
	"tidegate/internal/controller"
	"tidegate/internal/store"
)

type apiErrorBody struct {
	Error apiErrorDetail `json:"error"`
}

type apiErrorDetail struct {
	Code           string `json:"code"`
	Message        string `json:"message"`
	Field          string `json:"field,omitempty"`
	CurrentVersion string `json:"current_version,omitempty"`
}

type httpError struct {
	status int
	detail apiErrorDetail
}

func (e *httpError) Error() string { return e.detail.Code + ": " + e.detail.Message }

func newHTTPError(status int, code, msg string) error {
	return &httpError{status: status, detail: apiErrorDetail{Code: code, Message: msg}}
}

func writeError(w http.ResponseWriter, err error) {
	if err == nil {
		return
	}
	if _, ok := auth.AsError(err); ok {
		auth.WriteError(w, err)
		return
	}

	var he *httpError
	if errors.As(err, &he) {
		writeErrorDetail(w, he.status, he.detail)
		return
	}

	var ve *store.ValidationError
	if errors.As(err, &ve) {
		writeErrorDetail(w, http.StatusBadRequest, apiErrorDetail{
			Code:    "invalid_input",
			Message: ve.Error(),
			Field:   ve.Field,
		})
		return
	}

	var ce *store.ConflictError
	if errors.As(err, &ce) {
		writeErrorDetail(w, http.StatusConflict, apiErrorDetail{
			Code:           "conflict",
			Message:        ce.Error(),
			CurrentVersion: ce.Current,
		})
		return
	}

	var nf *store.NotFoundError
	if errors.As(err, &nf) {
		writeErrorDetail(w, http.StatusNotFound, apiErrorDetail{
			Code:    "not_found",
			Message: nf.Error(),
		})
		return
	}

	switch {
	case errors.Is(err, store.ErrInvalid), errors.Is(err, controller.ErrInvalidInput):
		writeErrorDetail(w, http.StatusBadRequest, apiErrorDetail{Code: "invalid_input", Message: err.Error()})
	case errors.Is(err, store.ErrNotFound):
		writeErrorDetail(w, http.StatusNotFound, apiErrorDetail{Code: "not_found", Message: err.Error()})
	case errors.Is(err, store.ErrConflict):
		writeErrorDetail(w, http.StatusConflict, apiErrorDetail{Code: "conflict", Message: err.Error()})
	case errors.Is(err, controller.ErrRunActive):
		writeErrorDetail(w, http.StatusConflict, apiErrorDetail{Code: "run_active", Message: err.Error()})
	case errors.Is(err, controller.ErrRunNotActive):
		writeErrorDetail(w, http.StatusConflict, apiErrorDetail{Code: "run_not_active", Message: err.Error()})
	case errors.Is(err, controller.ErrNoAgents):
		writeErrorDetail(w, http.StatusServiceUnavailable, apiErrorDetail{Code: "no_agents", Message: err.Error()})
	default:
		writeErrorDetail(w, http.StatusInternalServerError, apiErrorDetail{
			Code:    "internal_error",
			Message: "服务器内部错误",
		})
	}
}

func writeErrorDetail(w http.ResponseWriter, status int, detail apiErrorDetail) {
	h := w.Header()
	h.Set("Content-Type", "application/json; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(apiErrorBody{Error: detail})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	h := w.Header()
	h.Set("Content-Type", "application/json; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

const maxJSONBodyBytes = 16 << 20

func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxJSONBodyBytes)
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(dst); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			return newHTTPError(http.StatusRequestEntityTooLarge, "body_too_large", "请求体过大")
		}
		if errors.Is(err, io.EOF) {
			return newHTTPError(http.StatusBadRequest, "bad_json", "请求体不能为空，需要合法的 JSON")
		}
		return newHTTPError(http.StatusBadRequest, "bad_json", "请求体不是合法的 JSON")
	}
	return nil
}

// optField distinguishes omitted JSON fields from explicit null or value in PATCH payloads.
type optField[T any] struct {
	Set   bool
	Value T
}

func (o *optField[T]) UnmarshalJSON(b []byte) error {
	o.Set = true
	return json.Unmarshal(b, &o.Value)
}
