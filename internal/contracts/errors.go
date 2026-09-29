package contracts

import "errors"

// ErrorKind is the stable classification exposed by the transport layer.
type ErrorKind string

const (
	KindInvalid  ErrorKind = "invalid"
	KindNotFound ErrorKind = "not_found"
	KindConflict ErrorKind = "conflict"
	KindInternal ErrorKind = "internal"
)

// Error carries a machine-readable code without coupling domain code to HTTP.
type Error struct {
	Kind    ErrorKind
	Code    string
	Message string
	Cause   error
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	if e.Cause != nil {
		return e.Message + ": " + e.Cause.Error()
	}
	return e.Message
}

func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

func Invalid(code, message string) error {
	return &Error{Kind: KindInvalid, Code: code, Message: message}
}

func NotFound(code, message string) error {
	return &Error{Kind: KindNotFound, Code: code, Message: message}
}

func Conflict(code, message string) error {
	return &Error{Kind: KindConflict, Code: code, Message: message}
}

func Internal(code, message string, cause error) error {
	return &Error{Kind: KindInternal, Code: code, Message: message, Cause: cause}
}

func AsError(err error) (*Error, bool) {
	var target *Error
	if errors.As(err, &target) {
		return target, true
	}
	return nil, false
}
