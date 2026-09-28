package control

// Stable protocol error codes. These strings are part of the Agent Control
// Protocol surface and may be mapped onto HTTP/gRPC status independently.
const (
	ErrorCapabilityUnsupported    = "CAPABILITY_UNSUPPORTED"
	ErrorAttemptFenced            = "ATTEMPT_FENCED"
	ErrorOperationConflict        = "OPERATION_CONFLICT"
	ErrorResourceVersionConflict  = "RESOURCE_VERSION_CONFLICT"
	ErrorEventCursorExpired       = "EVENT_CURSOR_EXPIRED"
	ErrorExecutionUnverifiable    = "EXECUTION_UNVERIFIABLE"
	ErrorPermissionBlocked        = "PERMISSION_BLOCKED"
)
