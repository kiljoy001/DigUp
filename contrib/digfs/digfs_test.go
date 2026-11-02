package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func fileNode(id uint64, name string, content string) *fsNode {
	return &fsNode{id: id, name: name, mode: 0444, content: func() []byte { return []byte(content) }}
}

func dirNodeOf(kids ...*fsNode) *fsNode {
	return &fsNode{id: 1, name: "d", dir: true, mode: DMDIR | 0555,
		list: func() []*fsNode { return kids }}
}

// A directory read must never end in the middle of a stat entry: the client
// parses the whole reply and rejects the directory when it cannot.
func TestDirSliceKeepsEntriesWhole(t *testing.T) {
	var kids []*fsNode
	for i := 0; i < 40; i++ {
		kids = append(kids, fileNode(uint64(i+2), string(rune('a'+i%26))+"-file-with-a-longish-name.txt", "x"))
	}
	d := dirNodeOf(kids...)

	var all []byte
	var off uint64
	for {
		data := d.dirSlice(off, 97) // deliberately small, so reads must stop on boundaries
		if len(data) == 0 {
			break
		}
		all = append(all, data...)
		off += uint64(len(data))
	}

	count := 0
	for len(all) > 0 {
		_, n, err := parseDir(all)
		if err != nil {
			t.Fatalf("entry %d at offset %d: %v", count, len(all), err)
		}
		if n <= 0 || n > len(all) {
			t.Fatalf("entry %d consumed %d of %d bytes", count, n, len(all))
		}
		all = all[n:]
		count++
	}
	if count != len(kids) {
		t.Fatalf("parsed %d entries, want %d", count, len(kids))
	}
}

func TestDirSliceTooSmallRespondsEmpty(t *testing.T) {
	d := dirNodeOf(fileNode(2, "a-rather-long-file-name.txt", "x"))
	if got := d.dirSlice(0, 4); len(got) != 0 {
		t.Fatalf("got %d bytes for a count smaller than one entry", len(got))
	}
}

func TestParseDirRoundTrip(t *testing.T) {
	n := fileNode(42, "a-name.txt", "hello")
	st := encodeStat(n)
	d, used, err := parseDir(st)
	if err != nil {
		t.Fatal(err)
	}
	if used != len(st) {
		t.Fatalf("used %d of %d bytes", used, len(st))
	}
	if d.name != "a-name.txt" || d.length != 5 || d.qpath != 42 {
		t.Fatalf("parsed %+v", d)
	}
}

// pruneMirror must remove what the remote dropped, keep what was seen or
// deliberately not walked, and clean up empty directories.
func TestPruneMirror(t *testing.T) {
	dir := t.TempDir()
	write := func(rel string) {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0777); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0666); err != nil {
			t.Fatal(err)
		}
	}
	write("keep.txt")
	write("gone.txt")
	write("sub/keep-too.txt")
	write("sub/gone-too.txt")
	write("skipped/deep/file.txt")
	write("half.part")

	seen := map[string]bool{"keep.txt": true, "sub/keep-too.txt": true}
	keep := []string{"skipped/"}
	removed, err := pruneMirror(dir, seen, keep)
	if err != nil {
		t.Fatal(err)
	}
	// half.part is swept but is not a remote deletion, so it is not counted.
	if removed != 2 {
		t.Fatalf("removed %d, want 2", removed)
	}
	for _, p := range []string{"keep.txt", "sub/keep-too.txt", "skipped/deep/file.txt"} {
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(p))); err != nil {
			t.Fatalf("%s should have survived: %v", p, err)
		}
	}
	for _, p := range []string{"gone.txt", "sub/gone-too.txt", "half.part"} {
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(p))); !os.IsNotExist(err) {
			t.Fatalf("%s should be gone", p)
		}
	}
}

func TestMirrorStats(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "sub"), 0777)
	os.WriteFile(filepath.Join(dir, "a"), []byte("12345"), 0666)
	os.WriteFile(filepath.Join(dir, "sub", "b"), []byte("123"), 0666)
	files, bytes := mirrorStats(dir)
	if files != 2 || bytes != 8 {
		t.Fatalf("files=%d bytes=%d, want 2 and 8", files, bytes)
	}
}

// The parser is shipped twice: embedded here for the built-in view, and as a
// file for webbox.  They are the same program; keep them the same bytes.
func TestMarkdownCopiesMatch(t *testing.T) {
	other, err := os.ReadFile("../webbox/mkd.js")
	if err != nil {
		t.Skip("webbox copy not present")
	}
	if !bytes.Equal(other, mkdJS) {
		t.Fatal("contrib/digfs/mkd.js and contrib/webbox/mkd.js have drifted")
	}
}

func TestHighlight(t *testing.T) {
	got := highlight("…it «powers» the «long» board…")
	want := "…it <b>powers</b> the <b>long</b> board…"
	if got != want {
		t.Fatalf("highlight = %q, want %q", got, want)
	}
}
