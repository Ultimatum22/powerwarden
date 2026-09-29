package web

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"strings"
	"testing"

	assets "github.com/Ultimatum22/powerwarden/web"
)

// Vendored third-party files (htmx, fonts) are pinned by checksum
// (CLAUDE.md: "Pin htmx and fonts by checksum in the repo"); check the
// embedded copies against static/CHECKSUMS.txt.
func TestVendoredAssetsMatchChecksums(t *testing.T) {
	f, err := assets.Static.Open("static/CHECKSUMS.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	checked := 0
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		want, path, ok := strings.Cut(line, "  ")
		if !ok {
			t.Fatalf("malformed line %q", line)
		}
		data, err := fs.ReadFile(assets.Static, "static/"+path)
		if err != nil {
			t.Errorf("%s: %v", path, err)
			continue
		}
		sum := sha256.Sum256(data)
		if got := hex.EncodeToString(sum[:]); got != want {
			t.Errorf("%s: sha256 %s, pinned %s", path, got, want)
		}
		checked++
	}
	if checked < 5 {
		t.Fatalf("only %d files checked; expected htmx and the fonts", checked)
	}
}
