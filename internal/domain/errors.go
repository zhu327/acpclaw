package domain

import "errors"

var (
	ErrNoActiveSession           = errors.New("no active session")
	ErrNoActiveProcess           = errors.New("no active ACP process")
	ErrAgentOutputLimitExceeded  = errors.New("agent output exceeded ACP stdio limit")
	ErrLoadSessionNotSupported   = errors.New("agent does not support load_session")
	ErrSessionNotFound           = errors.New("session not found")
	ErrAgentCommandNotConfigured = errors.New("agent command not configured")
	ErrModelsNotSupported        = errors.New("agent does not support model switching")
	ErrModelNotFound             = errors.New("model not found in available models")
	ErrModesNotSupported         = errors.New("agent does not support mode switching")
	ErrModeNotFound              = errors.New("mode not found in available modes")
)
