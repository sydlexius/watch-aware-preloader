package mbapi

import "testing"

// FuzzValidateBaseURL checks that the base-URL trust-boundary validator never
// panics on arbitrary input. ValidateBaseURL parses attacker-influenced
// configuration and must reject anything unsafe with an error, never crash on a
// malformed URL. A returned error is the expected outcome for invalid input.
func FuzzValidateBaseURL(f *testing.F) {
	seeds := []string{
		"",
		"http://localhost:8096",
		"https://emby.example.com/",
		"https://user:pass@emby.example.com", // embedded credentials
		"mailto:someone@example.com",         // opaque form
		"ftp://example.com",                  // wrong scheme
		"http://",                            // no host
		"http://host:99999",                  // out-of-range port
		"http://host:notaport",               // non-numeric port
		"http://host/?q=1#frag",              // query + fragment
		"//no-scheme",
		"\x00\x01\x02",
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, raw string) {
		// Contract under test: no panic. When validation succeeds the returned
		// URL must be non-nil; otherwise any error is acceptable.
		u, err := ValidateBaseURL(raw)
		if err == nil && u == nil {
			t.Fatalf("ValidateBaseURL returned nil URL and nil error for %q", raw)
		}
	})
}
