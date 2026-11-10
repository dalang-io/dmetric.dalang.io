// Command relnotes renders the release notes of one version from
// internal/server/ui/releases.json (the file that also feeds the in-app
// release notes page), so GitHub release text and the app never drift.
//
//	go run ./tools/relnotes -version 2026.11.2 -check     # validate only
//	go run ./tools/relnotes -version 2026.11.2 > notes.md  # print markdown
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"regexp"
	"strings"
)

type group struct {
	Kind  string   `json:"kind"`
	Title string   `json:"title"`
	Items []string `json:"items"`
}

type release struct {
	Version  string  `json:"version"`
	Released bool    `json:"released"`
	Date     string  `json:"date"`
	Title    string  `json:"title"`
	Groups   []group `json:"groups"`
}

type file struct {
	Releases []release `json:"releases"`
}

var versionRe = regexp.MustCompile(`^20\d{2}\.\d+\.\d+$`)

var kindTitle = map[string]string{"added": "Baru", "changed": "Perubahan", "fixed": "Perbaikan", "security": "Keamanan"}

// find returns the release for version ("2026.11.2" or "v2026.11.2").
func find(data []byte, version string) (*release, error) {
	version = strings.TrimPrefix(strings.TrimSpace(version), "v")
	if !versionRe.MatchString(version) {
		return nil, fmt.Errorf("versi %q harus berbentuk YYYY.M.P, mis. 2026.11.2", version)
	}
	var f file
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("releases.json tidak valid: %w", err)
	}
	for i := range f.Releases {
		if f.Releases[i].Version == version {
			return &f.Releases[i], nil
		}
	}
	return nil, fmt.Errorf("releases.json tidak punya entri untuk %s; tambahkan catatan rilis dulu", version)
}

// check verifies the entry is final: marked released, dated, with notes.
func check(r *release) error {
	if !r.Released {
		return fmt.Errorf("entri %s masih \"released\": false di releases.json; set true saat rilis", r.Version)
	}
	if !regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`).MatchString(r.Date) {
		return fmt.Errorf("entri %s tidak punya tanggal (YYYY-MM-DD)", r.Version)
	}
	items := 0
	for _, g := range r.Groups {
		items += len(g.Items)
	}
	if items == 0 {
		return fmt.Errorf("entri %s tidak punya butir catatan", r.Version)
	}
	return nil
}

func render(r *release) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## v%s", r.Version)
	if r.Date != "" {
		fmt.Fprintf(&b, " — %s", r.Date)
	}
	b.WriteString("\n\n")
	if r.Title != "" {
		fmt.Fprintf(&b, "%s\n\n", r.Title)
	}
	for _, g := range r.Groups {
		if len(g.Items) == 0 {
			continue
		}
		title := g.Title
		if title == "" {
			title = kindTitle[g.Kind]
		}
		if title == "" {
			title = g.Kind
		}
		fmt.Fprintf(&b, "### %s\n\n", title)
		for _, item := range g.Items {
			fmt.Fprintf(&b, "- %s\n", item)
		}
		b.WriteString("\n")
	}
	return b.String()
}

func main() {
	version := flag.String("version", "", "versi, mis. 2026.11.2")
	path := flag.String("file", "internal/server/ui/releases.json", "lokasi releases.json")
	checkOnly := flag.Bool("check", false, "hanya validasi entri (tanpa mencetak)")
	flag.Parse()

	data, err := os.ReadFile(*path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "relnotes:", err)
		os.Exit(1)
	}
	r, err := find(data, *version)
	if err == nil {
		err = check(r)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "relnotes:", err)
		os.Exit(1)
	}
	if !*checkOnly {
		fmt.Print(render(r))
	}
}
