package status

import (
	"errors"
	"fmt"
	"io/fs"
)

type Level string

const (
	LevelOK   Level = "ok"
	LevelWarn Level = "warn"
	LevelFail Level = "fail"
)

type Kind string

const (
	KindConfig      Kind = "config"
	KindTemporary   Kind = "temporary"
	KindUnsupported Kind = "unsupported"
	KindNotFound    Kind = "not_found"
	KindConflict    Kind = "conflict"
	KindInternal    Kind = "internal"
)

type Error struct {
	Kind Kind
	Op   string
	Err  error
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	if e.Op == "" {
		return e.Err.Error()
	}
	return fmt.Sprintf("%s: %v", e.Op, e.Err)
}

func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func Wrap(kind Kind, op string, err error) error {
	if err == nil {
		return nil
	}
	return &Error{Kind: kind, Op: op, Err: err}
}

type Report struct {
	Level     Level
	Code      string
	Retryable bool
	Message   string
}

type temporary interface {
	Temporary() bool
}

func Classify(err error) Report {
	if err == nil {
		return Report{
			Level:   LevelOK,
			Code:    "ok",
			Message: "ok",
		}
	}

	var coded *Error
	if errors.As(err, &coded) {
		switch coded.Kind {
		case KindTemporary:
			return Report{
				Level:     LevelWarn,
				Code:      string(coded.Kind),
				Retryable: true,
				Message:   coded.Error(),
			}
		case KindUnsupported, KindNotFound:
			return Report{
				Level:   LevelWarn,
				Code:    string(coded.Kind),
				Message: coded.Error(),
			}
		case KindConfig, KindConflict, KindInternal:
			return Report{
				Level:   LevelFail,
				Code:    string(coded.Kind),
				Message: coded.Error(),
			}
		}
	}

	var temp temporary
	if errors.As(err, &temp) && temp.Temporary() {
		return Report{
			Level:     LevelWarn,
			Code:      string(KindTemporary),
			Retryable: true,
			Message:   err.Error(),
		}
	}

	if errors.Is(err, fs.ErrNotExist) {
		return Report{
			Level:   LevelWarn,
			Code:    string(KindNotFound),
			Message: err.Error(),
		}
	}

	return Report{
		Level:   LevelFail,
		Code:    string(KindInternal),
		Message: err.Error(),
	}
}
