package errcodes

var messagesEN = map[Code]Template{
	RealityHandshakeFail: {
		Title:   "Connection failed",
		Body:    "The server did not answer the Reality handshake. Check the public key, short id, and SNI.",
		Actions: []string{"Retry", "Diagnose", "ChangeServer", "ShowLog"},
	},
	TLSHandshakeFailed: {
		Title:   "TLS handshake failed",
		Body:    "The server rejected the TLS connection. Check SNI, ALPN, and the server certificate.",
		Actions: []string{"Retry", "Diagnose", "ShowLog"},
	},
	TCPConnectFailed: {
		Title:   "Server is not responding",
		Body:    "Could not open a TCP connection. The IP address or port may be blocked.",
		Actions: []string{"Retry", "ChangeServer", "Diagnose"},
	},
	DNSResolveFailed: {
		Title:   "DNS could not resolve the server",
		Body:    "The server hostname was not found. Check your internet connection and DNS settings.",
		Actions: []string{"Retry", "Diagnose"},
	},
	TUNAdapterFailed: {
		Title:   "TUN adapter did not start",
		Body:    "Windows did not allow creating the network adapter. Restart the client as administrator.",
		Actions: []string{"Retry", "ShowLog"},
	},
	KeyParseError: {
		Title:   "Key cannot be read",
		Body:    "The link or configuration contains unsupported or invalid fields.",
		Actions: []string{"EditServer", "ShowLog"},
	},
	InternalError: {
		Title:   "Internal error",
		Body:    "The client hit an unexpected error. Open the diagnostics package.",
		Actions: []string{"Diagnose", "ShowLog"},
	},
}
