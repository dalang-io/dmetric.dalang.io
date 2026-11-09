package server

import (
	"encoding/json"
	"regexp"
	"testing"
)

// releases.json feeds the release-notes page; a typo must fail the build,
// not ship a blank page.
func TestReleasesJSON(t *testing.T) {
	data, err := uiFiles.ReadFile("ui/releases.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Releases []struct {
			Version  string `json:"version"`
			Released bool   `json:"released"`
			Date     string `json:"date"`
			Groups   []struct {
				Kind  string   `json:"kind"`
				Items []string `json:"items"`
			} `json:"groups"`
		} `json:"releases"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("releases.json invalid: %v", err)
	}
	if len(doc.Releases) == 0 {
		t.Fatal("no releases")
	}
	version := regexp.MustCompile(`^20\d{2}\.\d+\.\d+$`) // matches the CI tag rule v20YY.M.P
	date := regexp.MustCompile(`^(\d{4}-\d{2}-\d{2})?$`)
	seen := map[string]bool{}
	for _, r := range doc.Releases {
		if !version.MatchString(r.Version) {
			t.Errorf("version %q does not match YYYY.M.P", r.Version)
		}
		if seen[r.Version] {
			t.Errorf("duplicate version %s", r.Version)
		}
		seen[r.Version] = true
		if !date.MatchString(r.Date) {
			t.Errorf("%s: bad date %q", r.Version, r.Date)
		}
		items := 0
		for _, g := range r.Groups {
			items += len(g.Items)
		}
		if items == 0 {
			t.Errorf("%s has no notes", r.Version)
		}
	}
	for _, f := range []string{"ui/releases.html", "ui/releases.js"} {
		if _, err := uiFiles.ReadFile(f); err != nil {
			t.Errorf("%s not embedded: %v", f, err)
		}
	}
}
