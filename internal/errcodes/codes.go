package errcodes

import "fmt"

type Code string

const (
	NoServerSelected     Code = "NO_SERVER_SELECTED"
	DNSResolveFailed     Code = "DNS_RESOLVE_FAILED"
	TCPConnectFailed     Code = "TCP_CONNECT_FAILED"
	TLSHandshakeFailed   Code = "TLS_HANDSHAKE_FAILED"
	RealityHandshakeFail Code = "REALITY_HANDSHAKE_FAILED"
	AuthRejected         Code = "AUTH_REJECTED"
	ProtocolMismatch     Code = "PROTOCOL_MISMATCH"
	KeyParseError        Code = "KEY_PARSE_ERROR"
	UnsupportedTransport Code = "UNSUPPORTED_TRANSPORT"
	SingboxStartFailed   Code = "SINGBOX_START_FAILED"
	TUNAdapterFailed     Code = "TUN_ADAPTER_FAILED"
	KillswitchActive     Code = "KILLSWITCH_ACTIVE"
	LicenseInvalid       Code = "LICENSE_INVALID"
	InternalError        Code = "INTERNAL_ERROR"
	UDPBlocked           Code = "UDP_BLOCKED"
	RoutingIssue         Code = "ROUTING_ISSUE"
)

type Error struct {
	Code    Code   `json:"code"`
	Stage   string `json:"stage"`
	Message string `json:"message"`
	Hint    string `json:"hint"`
	Cause   error  `json:"-"`
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	if e.Cause != nil {
		return fmt.Sprintf("%s at %s: %s: %v", e.Code, e.Stage, e.Message, e.Cause)
	}
	return fmt.Sprintf("%s at %s: %s", e.Code, e.Stage, e.Message)
}

func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

func New(code Code, stage, message, hint string, cause error) *Error {
	return &Error{Code: code, Stage: stage, Message: message, Hint: hint, Cause: cause}
}
