package tesla

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func tokenAuthHandler(refresh string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth2/v3/token", func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewEncoder(w).Encode(map[string]any{
			"access_token": "access", "refresh_token": refresh, "expires_in": 3600,
		}); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	})
	return mux
}

func TestRefreshPersistsRotatedToken(t *testing.T) {
	for _, tc := range []struct {
		name     string
		returned string
		wantFile string
	}{
		{name: "rotated token is saved", returned: "rotated-refresh", wantFile: "rotated-refresh"},
		{name: "unchanged token is not rewritten", returned: "test-refresh", wantFile: "previous"},
		{name: "omitted token is not rewritten", returned: "", wantFile: "previous"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := newTestClient(t, http.NewServeMux(), tokenAuthHandler(tc.returned))
			c.tokenPath = filepath.Join(t.TempDir(), "tesla-refresh-token")
			if err := os.WriteFile(c.tokenPath, []byte("previous"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := c.refreshAccessToken(context.Background()); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(c.tokenPath)
			if err != nil {
				t.Fatal(err)
			}
			if string(data) != tc.wantFile {
				t.Fatalf("token file = %q, want %q", data, tc.wantFile)
			}
			info, err := os.Stat(c.tokenPath)
			if err != nil {
				t.Fatal(err)
			}
			if perm := info.Mode().Perm(); perm != 0o600 {
				t.Fatalf("token file mode = %o, want 600", perm)
			}
		})
	}
}

func TestResolveRefreshToken(t *testing.T) {
	for _, tc := range []struct {
		name       string
		file       *string
		configured string
		wantToken  string
		wantSource string
	}{
		{name: "file wins over stale environment token", file: ptr("newest\n"), configured: "stale", wantToken: "newest", wantSource: "file"},
		{name: "environment used when file missing", configured: " env ", wantToken: "env", wantSource: "environment"},
		{name: "environment used when file blank", file: ptr("  \n"), configured: "env", wantToken: "env", wantSource: "environment"},
		{name: "nothing configured", wantToken: "", wantSource: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "tesla-refresh-token")
			if tc.file != nil {
				if err := os.WriteFile(path, []byte(*tc.file), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			token, source, err := ResolveRefreshToken(path, tc.configured)
			if err != nil {
				t.Fatal(err)
			}
			if token != tc.wantToken || source != tc.wantSource {
				t.Fatalf("got (%q, %q), want (%q, %q)", token, source, tc.wantToken, tc.wantSource)
			}
		})
	}
}

func TestResolveRefreshTokenUnreadableFileFallsBack(t *testing.T) {
	dir := t.TempDir() // a directory cannot be read as a token file
	token, source, err := ResolveRefreshToken(dir, "env")
	if err == nil {
		t.Fatal("expected read error to be reported")
	}
	if token != "env" || source != "environment" {
		t.Fatalf("got (%q, %q), want environment fallback", token, source)
	}
}

func TestWriteRefreshTokenFileRequiresPath(t *testing.T) {
	if err := WriteRefreshTokenFile(" ", "token"); err == nil {
		t.Fatal("expected error for empty path")
	}
}

func ptr(s string) *string { return &s }
