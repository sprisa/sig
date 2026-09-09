package signoz

import (
	"net/http"
	"strings"
)

const maxCustomHeaderBytes = 64 << 10
const maxCustomHeaders = 64

// ParseCustomHeaders implements the MCP's comma-separated Name:Value format.
// The returned map is owned by the caller. Errors never include input text:
// even an invalid name or a missing delimiter can contain a credential.
func ParseCustomHeaders(raw string) (http.Header, error) {
	if len(raw) > maxCustomHeaderBytes {
		return nil, usage("SIGNOZ_CUSTOM_HEADERS exceeds 64 KiB")
	}
	if !validHeaderValue(raw) {
		return nil, usage("SIGNOZ_CUSTOM_HEADERS contains a forbidden control character")
	}
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	headers := make(http.Header)
	for pair := range strings.SplitSeq(raw, ",") {
		name, value, ok := strings.Cut(pair, ":")
		name, value = strings.TrimSpace(name), strings.TrimSpace(value)
		if !ok || !validHeaderName(name) {
			return nil, usage("SIGNOZ_CUSTOM_HEADERS must contain comma-separated Name:Value entries with valid HTTP header names")
		}
		name = http.CanonicalHeaderKey(name)
		if reservedHeader(name) {
			return nil, usage("SIGNOZ_CUSTOM_HEADERS cannot override CLI-owned, routing, or HTTP transport headers; see the custom-header documentation")
		}
		if _, exists := headers[name]; exists {
			return nil, usage("SIGNOZ_CUSTOM_HEADERS contains a duplicate header name (names are case-insensitive)")
		}
		if len(headers) == maxCustomHeaders {
			return nil, usage("SIGNOZ_CUSTOM_HEADERS supports at most 64 headers")
		}
		headers.Set(name, value)
	}
	return headers, nil
}

// RFC 9110 field names are ASCII tokens; values may contain HTAB and obs-text,
// but never CR/LF, DEL, or other control bytes. Check before trimming whitespace.
func validHeaderName(name string) bool {
	if name == "" {
		return false
	}
	for _, b := range []byte(name) {
		if b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || strings.ContainsRune("!#$%&'*+-.^_`|~", rune(b)) {
			continue
		}
		return false
	}
	return true
}

func validHeaderValue(value string) bool {
	for _, b := range []byte(value) {
		if b < 0x20 && b != '\t' || b == 0x7f {
			return false
		}
	}
	return true
}

func reservedHeader(name string) bool {
	switch name {
	case "Signoz-Api-Key", "X-Signoz-Url", "Host", "Accept", "Accept-Encoding", "Content-Type", "Content-Length", "User-Agent",
		"Connection", "Proxy-Connection", "Proxy-Authorization", "Transfer-Encoding", "Trailer", "Te", "Upgrade", "Expect":
		return true
	}
	return false
}
