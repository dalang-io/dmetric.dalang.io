package main

import (
	"strings"
	"testing"
)

const sample = `{"releases":[
 {"version":"2026.11.2","released":true,"date":"2026-12-01","title":"Judul","groups":[
   {"kind":"added","items":["Satu","Dua"]},{"kind":"fixed","items":["Tiga"]},{"kind":"changed","items":[]}]},
 {"version":"2026.11.3","released":false,"date":"","title":"Draf","groups":[{"kind":"added","items":["x"]}]},
 {"version":"2026.11.4","released":true,"date":"2026-12-02","title":"Kosong","groups":[]}
]}`

func TestFindAcceptsTagForm(t *testing.T) {
	for _, v := range []string{"2026.11.2", "v2026.11.2", " v2026.11.2 "} {
		if r, err := find([]byte(sample), v); err != nil || r.Version != "2026.11.2" {
			t.Fatalf("find(%q) = %v, %v", v, r, err)
		}
	}
	if _, err := find([]byte(sample), "2026.11.9"); err == nil {
		t.Fatal("missing version must be an error")
	}
	if _, err := find([]byte(sample), "latest"); err == nil {
		t.Fatal("malformed version must be an error")
	}
	if _, err := find([]byte("{"), "2026.11.2"); err == nil {
		t.Fatal("broken json must be an error")
	}
}

func TestCheckRefusesDraftsAndEmptyNotes(t *testing.T) {
	ok, _ := find([]byte(sample), "2026.11.2")
	if err := check(ok); err != nil {
		t.Fatal(err)
	}
	draft, _ := find([]byte(sample), "2026.11.3")
	if err := check(draft); err == nil || !strings.Contains(err.Error(), "released") {
		t.Fatalf("an unreleased entry must not pass: %v", err)
	}
	empty, _ := find([]byte(sample), "2026.11.4")
	if err := check(empty); err == nil {
		t.Fatal("an entry without notes must not pass")
	}
}

func TestRenderMarkdown(t *testing.T) {
	r, _ := find([]byte(sample), "2026.11.2")
	got := render(r)
	for _, want := range []string{"## v2026.11.2 — 2026-12-01", "Judul", "### Baru", "- Satu", "- Dua", "### Perbaikan", "- Tiga"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "### Perubahan") {
		t.Error("empty groups must not render a heading")
	}
}
