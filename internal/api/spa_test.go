package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSPAHandler exercises the SPA handler directly (without the ServeMux,
// which would already redirect dot segments) so the handler's own path
// confinement is what is under test.
func TestSPAHandler(t *testing.T) {
	base := t.TempDir()
	dist := filepath.Join(base, "dist")
	if err := os.Mkdir(dist, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(path, content string) {
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(dist, "index.html"), "<html>spa</html>")
	write(filepath.Join(dist, "app.js"), "console.log(1)")
	// A file next to, not inside, the dist dir must stay invisible.
	write(filepath.Join(base, "secret.txt"), "top secret")

	h := spaHandler(dist)
	get := func(target string) (*http.Response, string) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
		res := rec.Result()
		body, _ := io.ReadAll(res.Body)
		return res, string(body)
	}

	t.Run("static file", func(t *testing.T) {
		res, body := get("/app.js")
		if res.StatusCode != http.StatusOK || body != "console.log(1)" {
			t.Fatalf("got %d %q", res.StatusCode, body)
		}
	})
	t.Run("root advertises service-desc", func(t *testing.T) {
		res, body := get("/")
		if res.StatusCode != http.StatusOK || !strings.Contains(body, "spa") {
			t.Fatalf("got %d %q", res.StatusCode, body)
		}
		if res.Header.Get("Link") != serviceDescLink {
			t.Fatalf("Link header = %q", res.Header.Get("Link"))
		}
	})
	t.Run("client route falls back to index", func(t *testing.T) {
		res, body := get("/profiles")
		if res.StatusCode != http.StatusOK || !strings.Contains(body, "spa") {
			t.Fatalf("got %d %q", res.StatusCode, body)
		}
		if res.Header.Get("Link") != serviceDescLink {
			t.Fatalf("Link header = %q", res.Header.Get("Link"))
		}
	})
	// The traversal cases pair a file that exists outside dist with one that
	// does not: the responses must be identical, otherwise the handler is an
	// existence oracle for the rest of the filesystem (the old handler
	// answered the two with different status codes).
	for _, tc := range []struct{ exists, missing string }{
		{"/../secret.txt", "/../nope.txt"},
		{"/%2e%2e/secret.txt", "/%2e%2e/nope.txt"},
		{"/sub/../../secret.txt", "/sub/../../nope.txt"},
	} {
		t.Run("traversal "+tc.exists, func(t *testing.T) {
			res, body := get(tc.exists)
			if strings.Contains(body, "top secret") {
				t.Fatalf("%s leaked a file outside dist: %d %q", tc.exists, res.StatusCode, body)
			}
			ref, refBody := get(tc.missing)
			if res.StatusCode != ref.StatusCode || body != refBody {
				t.Fatalf("existence oracle: %s -> %d %q, %s -> %d %q",
					tc.exists, res.StatusCode, body, tc.missing, ref.StatusCode, refBody)
			}
		})
	}
}
