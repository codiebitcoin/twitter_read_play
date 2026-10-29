package twitter

import "testing"

func TestParseXCreds(t *testing.T) {
	tok, ct0 := ParseXCreds("auth_token=abc; ct0=def", "")
	if tok != "abc" || ct0 != "def" {
		t.Fatalf("cookie key: %q %q", tok, ct0)
	}
	tok, ct0 = ParseXCreds("bare-token", "csrf")
	if tok != "bare-token" || ct0 != "csrf" {
		t.Fatalf("bare: %q %q", tok, ct0)
	}
}
