package server

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

type redactedStorageTestError struct{}

func (redactedStorageTestError) Error() string { return "DO_NOT_LOG_SECRET_STATEMENT_VALUE" }
func (redactedStorageTestError) Code() int     { return 19 }
func TestStorageErrorDiagnosticDoesNotLogRawValues(t *testing.T) {
	var output bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&output, nil)))
	defer slog.SetDefault(old)
	dbErr(redactedStorageTestError{})
	text := output.String()
	if !strings.Contains(text, "sqlite_code=19") {
		t.Fatal("missing storage error classification")
	}
	if strings.Contains(text, "DO_NOT_LOG") {
		t.Fatal("raw error leaked")
	}
}
