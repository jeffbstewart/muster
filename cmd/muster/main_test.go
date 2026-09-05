package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCacheHolds(t *testing.T) {
	names := []string{
		"registry.example/app@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"registry.example/app:v1",
		"registry.example/other:v2",
	}
	cases := []struct {
		entry string
		want  bool
	}{
		{"registry.example/app:v1@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", true},
		{"registry.example/app:v9@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", true}, // digest wins over tag
		{"registry.example/app@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", false},
		{"registry.example/other:v2", true},
		{"registry.example/other:v3", false},
	}
	for _, c := range cases {
		if got := cacheHolds(names, c.entry); got != c.want {
			t.Errorf("cacheHolds(%q) = %v, want %v", c.entry, got, c.want)
		}
	}
}

func TestManifestLists(t *testing.T) {
	entries := []string{
		"registry.example/app:v1@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"registry.example/plain:v2",
	}
	cases := []struct {
		img  string
		want bool
	}{
		{"registry.example/app:v1@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", true},
		{"registry.example/renamed@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", true}, // same digest
		{"registry.example/app:v1", true}, // covered by the pinned entry for the same repo:tag
		{"registry.example/plain:v2", true},
		{"registry.example/plain:v3", false},
	}
	for _, c := range cases {
		if got := manifestLists(entries, c.img); got != c.want {
			t.Errorf("manifestLists(%q) = %v, want %v", c.img, got, c.want)
		}
	}
}

func TestReadManifestSkipsCommentsAndBlanks(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "m.txt")
	if err := os.WriteFile(p, []byte("# header\n\nb.example/img:v1\na.example/img:v2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := readManifest(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "a.example/img:v2" {
		t.Fatalf("readManifest = %v", got)
	}
}

func TestRenderMetricsEscapesAndCounts(t *testing.T) {
	s := reconcileState{
		when:     time.Unix(100, 0),
		ok:       true,
		entries:  3,
		reported: map[string]int{"w1": 40},
		absent:   map[string][]string{"w1": {`reg/img:v1`}},
		unlisted: map[string][]string{"kube-system": {`reg/x"y:v2`}},
	}
	out := string(renderMetrics(s))
	for _, want := range []string{
		"muster_reconcile_success 1",
		"muster_manifest_entries 3",
		`muster_node_images_reported{node="w1"} 40`,
		`muster_absent_count{node="w1"} 1`,
		`muster_absent{node="w1",image="reg/img:v1"} 1`,
		`muster_unlisted{namespace="kube-system",image="reg/x\"y:v2"} 1`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("metrics output missing %q\n%s", want, out)
		}
	}
}
