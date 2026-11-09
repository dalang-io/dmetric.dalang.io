package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"2026.12.0", "2026.11.0", 1},
		{"v2026.11.0", "2026.11.0", 0},
		{"2026.11", "2026.11.0", 0},
		{"2027.1.0", "2026.12.9", 1},
		{"2026.9.0", "2026.10.0", -1},
	}
	for _, test := range cases {
		if got := compareVersions(test.a, test.b); got != test.want {
			t.Errorf("compareVersions(%q,%q)=%d, want %d", test.a, test.b, got, test.want)
		}
	}
}

func TestChecksumFor(t *testing.T) {
	hash := strings.Repeat("a", 64)
	data := []byte(hash + "  dmetric-linux-amd64\n" + strings.Repeat("f", 64) + "  other\n")
	if got := checksumFor(data, "dmetric-linux-amd64"); got != hash {
		t.Fatalf("checksumFor = %q", got)
	}
	if got := checksumFor(data, "missing"); got != "" {
		t.Fatalf("unexpected checksum for missing asset: %q", got)
	}
}

func TestAssetName(t *testing.T) {
	name := assetName()
	if name == "" || !strings.HasPrefix(name, "dmetric-") {
		t.Fatalf("assetName = %q", name)
	}
}

func TestCheckAndDownload(t *testing.T) {
	content := []byte("new binary")
	sum := sha256.Sum256(content)
	hash := hex.EncodeToString(sum[:])
	mux := http.NewServeMux()
	mux.HandleFunc("/asset", func(w http.ResponseWriter, r *http.Request) {
		w.Write(content)
	})
	mux.HandleFunc("/sha", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(hash + "  " + assetName() + "\n"))
	})
	mux.HandleFunc("/release", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"tag_name": "v2026.12.0",
			"html_url": "https://example.test/release",
			"body":     "notes",
			"assets": []map[string]any{
				{"name": assetName(), "browser_download_url": "http://" + r.Host + "/asset", "size": len(content)},
				{"name": "SHA256SUMS", "browser_download_url": "http://" + r.Host + "/sha", "size": 80},
			},
		})
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	m := New(t.TempDir(), "2026.11.0", "test/repo")
	m.apiURL = server.URL + "/release"
	info := m.Check(context.Background(), true)
	if info.Error != "" {
		t.Fatal(info.Error)
	}
	if !info.UpdateAvailable || info.LatestVersion != "2026.12.0" || info.AssetName != assetName() {
		t.Fatalf("unexpected info: %+v", info)
	}
	if !info.Checksum {
		t.Fatal("checksum asset not detected")
	}

	dir := t.TempDir()
	target := filepath.Join(dir, "asset")
	if err := m.downloadFile(context.Background(), server.URL+"/asset", target, hash); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(content) {
		t.Fatalf("download content = %q", got)
	}
}

func TestCheckNotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer server.Close()
	m := New(t.TempDir(), "2026.11.0", "test/repo")
	m.apiURL = server.URL
	info := m.Check(context.Background(), true)
	if info.Error == "" {
		t.Fatal("expected a friendly error for a missing release")
	}
}
