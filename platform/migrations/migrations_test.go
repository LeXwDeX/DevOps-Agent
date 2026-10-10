package migrations

import (
	"regexp"
	"strings"
	"testing"
)

func TestFiles(t *testing.T) {
	name := regexp.MustCompile(`^[0-9]{4}_[a-z0-9_]+\.sql$`)
	for _, db := range Databases() {
		fs, err := Files(db)
		if err != nil || len(fs) == 0 {
			t.Fatalf("%s: %v, %d files", db, err, len(fs))
		}
		for i, f := range fs {
			if !name.MatchString(f.Name) || strings.TrimSpace(f.SQL) == "" {
				t.Errorf("%s/%s: bad name or empty", db, f.Name)
			}
			if i > 0 && fs[i-1].Name >= f.Name {
				t.Errorf("%s: files not sorted: %s, %s", db, fs[i-1].Name, f.Name)
			}
		}
	}
	if _, err := Files("nope"); err == nil {
		t.Error("unknown database accepted")
	}
}
