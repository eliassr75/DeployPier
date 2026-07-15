package status

import (
	"errors"
	"testing"
)

type tempError struct{}

func (tempError) Error() string   { return "temporary" }
func (tempError) Temporary() bool { return true }

func TestClassifyNil(t *testing.T) {
	report := Classify(nil)
	if report.Level != LevelOK {
		t.Fatalf("expected ok level, got %s", report.Level)
	}
}

func TestClassifyUnsupported(t *testing.T) {
	report := Classify(Wrap(KindUnsupported, "probe", errors.New("missing transport")))
	if report.Level != LevelWarn {
		t.Fatalf("expected warn level, got %s", report.Level)
	}
	if report.Retryable {
		t.Fatalf("expected unsupported to be non-retryable")
	}
}

func TestClassifyTemporary(t *testing.T) {
	report := Classify(tempError{})
	if report.Level != LevelWarn {
		t.Fatalf("expected warn level, got %s", report.Level)
	}
	if !report.Retryable {
		t.Fatalf("expected temporary error to be retryable")
	}
}

func TestClassifyConfig(t *testing.T) {
	report := Classify(Wrap(KindConfig, "load config", errors.New("invalid")))
	if report.Level != LevelFail {
		t.Fatalf("expected fail level, got %s", report.Level)
	}
}
