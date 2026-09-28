package control

// ErrorCode is a stable machine-readable Agent Control Protocol error.
type ErrorCode string

const (
	ErrorCapabilityUnsupported    ErrorCode = "CAPABILITY_UNSUPPORTED"
	ErrorAttemptFenced            ErrorCode = "ATTEMPT_FENCED"
	ErrorOperationConflict        ErrorCode = "OPERATION_CONFLICT"
	ErrorResourceVersionConflict  ErrorCode = "RESOURCE_VERSION_CONFLICT"
	ErrorEventCursorExpired       ErrorCode = "EVENT_CURSOR_EXPIRED"
	ErrorExecutionUnverifiable    ErrorCode = "EXECUTION_UNVERIFIABLE"
	ErrorPermissionBlocked        ErrorCode = "PERMISSION_BLOCKED"
)

func (c ErrorCode) String() string { return string(c) }
