package store

import (
	"errors"
	"fmt"
)

// Sentinel errors for errors.Is. Every typed error of this package matches
// exactly one of them, so callers can map errors to HTTP status codes
// (404, 409, 400) without inspecting the concrete type.
var (
	ErrNotFound = errors.New("store: not found")
	ErrConflict = errors.New("store: conflict")
	ErrInvalid  = errors.New("store: invalid input")
)

// Kind names the type of a stored item in errors.
type Kind string

// Item kinds.
const (
	KindProject     Kind = "project"
	KindFolder      Kind = "folder"
	KindAPI         Kind = "api"
	KindNode        Kind = "node" // a folder or an API
	KindEnvironment Kind = "environment"
	KindScenario    Kind = "scenario"
	KindFile        Kind = "file"
	KindRun         Kind = "run"
	KindAlert       Kind = "alert"
	KindDebug       Kind = "debug"
)

// Label returns the user facing name of the kind.
func (k Kind) Label() string {
	switch k {
	case KindProject:
		return "项目"
	case KindFolder:
		return "文件夹"
	case KindAPI:
		return "接口"
	case KindNode:
		return "接口或文件夹"
	case KindEnvironment:
		return "环境"
	case KindScenario:
		return "场景"
	case KindFile:
		return "数据文件"
	case KindRun:
		return "压测记录"
	case KindAlert:
		return "告警"
	case KindDebug:
		return "调试记录"
	}
	return string(k)
}

// NotFoundError reports that an item does not exist.
type NotFoundError struct {
	Kind Kind
	ID   string
}

func (e *NotFoundError) Error() string {
	if e.ID == "" {
		return e.Kind.Label() + "不存在"
	}
	return fmt.Sprintf("%s %q 不存在", e.Kind.Label(), e.ID)
}

// Is makes errors.Is(err, ErrNotFound) true.
func (e *NotFoundError) Is(target error) bool { return target == ErrNotFound }

// ConflictError reports that an item changed since the client read it (the
// expected version does not match) or that it already exists.
type ConflictError struct {
	Kind Kind
	ID   string
	// Current is the version currently stored, empty when unknown.
	Current string
	// Msg overrides the default message.
	Msg string
}

func (e *ConflictError) Error() string {
	if e.Msg != "" {
		return e.Msg
	}
	return e.Kind.Label() + "已被其他人修改，请刷新后重试"
}

// Is makes errors.Is(err, ErrConflict) true.
func (e *ConflictError) Is(target error) bool { return target == ErrConflict }

// ValidationError reports invalid input.
type ValidationError struct {
	// Field is the offending field, for example "name" or "steps[2].api_id".
	Field string
	Msg   string
}

func (e *ValidationError) Error() string {
	if e.Field == "" {
		return e.Msg
	}
	return e.Field + "：" + e.Msg
}

// Is makes errors.Is(err, ErrInvalid) true.
func (e *ValidationError) Is(target error) bool { return target == ErrInvalid }

func notFound(k Kind, id string) error { return &NotFoundError{Kind: k, ID: id} }

func invalid(field, format string, args ...any) error {
	return &ValidationError{Field: field, Msg: fmt.Sprintf(format, args...)}
}

// withField returns a validation error re-labelled with field.
func withField(field string, err error) error {
	var ve *ValidationError
	if errors.As(err, &ve) {
		return &ValidationError{Field: field, Msg: ve.Msg}
	}
	return err
}

// checkVersion verifies the expected version of an update. Updates must
// always carry the version the client read.
func checkVersion(k Kind, id, expected, current string) error {
	if expected == "" {
		return invalid("version", "缺少版本号，请刷新后重试")
	}
	if expected != current {
		return &ConflictError{Kind: k, ID: id, Current: current}
	}
	return nil
}

// checkDeleteVersion verifies the expected version of a delete. An empty
// expected version deletes unconditionally.
func checkDeleteVersion(k Kind, id, expected, current string) error {
	if expected != "" && expected != current {
		return &ConflictError{Kind: k, ID: id, Current: current}
	}
	return nil
}
