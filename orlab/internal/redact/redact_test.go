package redact

import (
	"net/http"
	"strings"
	"testing"
)

const fakeKey = "sk-or-v1-0123456789abcdefABCDEF_-x"

func TestBytes(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"bare key", fakeKey, "[REDACTED]"},
		{"key in json body", `{"api_key":"` + fakeKey + `"}`, `{"api_key":"[REDACTED]"}`},
		{"key in bearer header line", "Authorization: Bearer " + fakeKey, "Authorization: Bearer [REDACTED]"},
		{"key in url query", "https://evil.example/?k=" + fakeKey + "&x=1", "https://evil.example/?k=[REDACTED]&x=1"},
		{"two keys", fakeKey + " and " + fakeKey, "[REDACTED] and [REDACTED]"},
		{"key with only dashes and underscores", "sk-or-___---___", "[REDACTED]"},
		// Trailing chars in the key alphabet are consumed too — over-redaction
		// is the safe direction.
		{"key mid-word", "prefixsk-or-abc123suffix!", "prefix[REDACTED]!"},
		{"key at end of truncated record", `{"h":"` + fakeKey, `{"h":"[REDACTED]`},
		{"key inside sse data line", "data: {\"key\":\"" + fakeKey + "\"}\n\n", "data: {\"key\":\"[REDACTED]\"}\n\n"},
		{"no key", `{"model":"x-ai/grok-4.6"}`, `{"model":"x-ai/grok-4.6"}`},
		{"openai key untouched", "sk-abc123", "sk-abc123"},
		{"empty", "", ""},
		// The regex is greedy over the key alphabet; trailing punctuation stops it.
		{"key followed by quote", `"` + fakeKey + `"`, `"[REDACTED]"`},
		{"unicode around key", "🔑" + fakeKey + "🔑", "🔑[REDACTED]🔑"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := string(Bytes([]byte(tt.in))); got != tt.want {
				t.Errorf("Bytes(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestHeaders(t *testing.T) {
	h := http.Header{}
	h.Set("Authorization", "Bearer "+fakeKey)
	h.Set("Content-Type", "application/json")
	h.Set("Cookie", "session=secret")
	h.Add("Set-Cookie", "a=1")
	h.Add("Set-Cookie", "b=2")
	h.Set("X-Api-Key", fakeKey)
	h.Set("X-Debug-Echo", "contains "+fakeKey+" inline") // key leaked into a non-sensitive header

	got := Headers(h)

	if v := got.Get("Authorization"); v != "[REDACTED]" {
		t.Errorf("Authorization = %q", v)
	}
	if v := got.Get("Cookie"); v != "[REDACTED]" {
		t.Errorf("Cookie = %q", v)
	}
	if vs := got["Set-Cookie"]; len(vs) != 1 || vs[0] != "[REDACTED]" {
		t.Errorf("Set-Cookie = %v", vs)
	}
	if v := got.Get("X-Api-Key"); v != "[REDACTED]" {
		t.Errorf("X-Api-Key = %q", v)
	}
	if v := got.Get("X-Debug-Echo"); strings.Contains(v, "sk-or-") {
		t.Errorf("key survived in non-sensitive header: %q", v)
	}
	if v := got.Get("Content-Type"); v != "application/json" {
		t.Errorf("Content-Type mangled: %q", v)
	}
	// Original must be untouched.
	if v := h.Get("Authorization"); v != "Bearer "+fakeKey {
		t.Errorf("input header mutated: %q", v)
	}
}

func TestHeadersCaseInsensitive(t *testing.T) {
	// Bypass http.Header canonicalization to simulate a hostile map.
	h := http.Header{"aUtHoRiZaTiOn": {"Bearer " + fakeKey}}
	got := Headers(h)
	for name, vs := range got {
		for _, v := range vs {
			if strings.Contains(v, "sk-or-") {
				t.Errorf("key survived in header %q: %q", name, v)
			}
		}
	}
}

func TestVerify(t *testing.T) {
	if err := Verify([]byte("clean body, no secrets")); err != nil {
		t.Errorf("Verify(clean) = %v", err)
	}
	if err := Verify([]byte(`{"k":"` + fakeKey + `"}`)); err == nil {
		t.Error("Verify(dirty) = nil, want error")
	}
	if err := Verify(Bytes([]byte(`{"k":"` + fakeKey + `"}`))); err != nil {
		t.Errorf("Verify(Bytes(dirty)) = %v, want nil", err)
	}
}

func TestFingerprint(t *testing.T) {
	fp := Fingerprint(fakeKey)
	if strings.Contains(fp, "sk-or-") {
		t.Errorf("fingerprint leaks key prefix: %q", fp)
	}
	if !strings.Contains(fp, fakeKey[len(fakeKey)-4:]) {
		t.Errorf("fingerprint missing tail: %q", fp)
	}
	if Fingerprint(fakeKey) != fp {
		t.Error("fingerprint not deterministic")
	}
	if Fingerprint("") != "(no key)" {
		t.Errorf("empty key fingerprint = %q", Fingerprint(""))
	}
	if Fingerprint("abc") == fp {
		t.Error("distinct keys share fingerprint")
	}
}
