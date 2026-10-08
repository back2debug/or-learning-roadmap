// Package redact scrubs credentials from anything destined for disk or logs.
//
// The contract is fail-closed: callers that persist data must call Verify on
// the scrubbed output and drop the record if it errors. Scrubbing happens
// before the write, never as a post-processing pass.
package redact

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"regexp"
	"strings"
)

const placeholder = "[REDACTED]"

// keyPattern matches OpenRouter API keys anywhere in a byte stream. The
// charset includes - and _ per the observed key alphabet.
var keyPattern = regexp.MustCompile(`sk-or-[A-Za-z0-9\-_]+`)

// sensitiveHeaders are removed wholesale from any serialized header set.
// Matching is case-insensitive, as HTTP headers are.
var sensitiveHeaders = map[string]bool{
	"authorization":       true,
	"proxy-authorization": true,
	"cookie":              true,
	"set-cookie":          true,
	"x-api-key":           true,
}

// Bytes returns b with every embedded key replaced by a placeholder.
func Bytes(b []byte) []byte {
	return keyPattern.ReplaceAll(b, []byte(placeholder))
}

// String is Bytes for strings.
func String(s string) string {
	return keyPattern.ReplaceAllString(s, placeholder)
}

// Headers returns a copy of h with sensitive headers replaced by a
// placeholder value (the header name is kept so transcripts show it was sent)
// and key material scrubbed from all remaining values.
func Headers(h http.Header) http.Header {
	out := make(http.Header, len(h))
	for name, values := range h {
		if sensitiveHeaders[strings.ToLower(name)] {
			out[name] = []string{placeholder}
			continue
		}
		clean := make([]string, len(values))
		for i, v := range values {
			clean[i] = String(v)
		}
		out[name] = clean
	}
	return out
}

// Verify errors if b still contains anything that looks like a key. Callers
// persisting data must treat an error as "do not write this record".
func Verify(b []byte) error {
	if loc := keyPattern.FindIndex(b); loc != nil {
		return fmt.Errorf("redact: residual credential at byte offset %d", loc[0])
	}
	return nil
}

// Fingerprint identifies a key without revealing it: the last 4 characters
// plus an 8-hex-char SHA-256 prefix. Safe to log.
func Fingerprint(key string) string {
	if key == "" {
		return "(no key)"
	}
	sum := sha256.Sum256([]byte(key))
	tail := key
	if len(key) > 4 {
		tail = key[len(key)-4:]
	}
	return fmt.Sprintf("…%s (sha256:%s)", tail, hex.EncodeToString(sum[:])[:8])
}
