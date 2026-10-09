package errcode

import (
	"errors"
	"fmt"
	"regexp"
	"testing"
)

func TestCatalog(t *testing.T) {
	re := regexp.MustCompile(`^[a-z]+\.[a-z_]+$`)
	seen := map[Code]bool{}
	all := All()
	if len(all) == 0 {
		t.Fatal("empty catalog")
	}
	for i, in := range all {
		if !re.MatchString(string(in.Code)) {
			t.Errorf("bad code format: %q", in.Code)
		}
		if seen[in.Code] {
			t.Errorf("duplicate: %q", in.Code)
		}
		seen[in.Code] = true
		if in.Meaning == "" {
			t.Errorf("no meaning: %q", in.Code)
		}
		if i > 0 && all[i-1].Code >= in.Code {
			t.Errorf("not sorted at %q", in.Code)
		}
		if got, ok := Lookup(in.Code); !ok || got != in {
			t.Errorf("Lookup(%q) mismatch", in.Code)
		}
	}
	if _, ok := Lookup("core.nope"); ok {
		t.Error("unknown code found")
	}
}

func TestErrorBehaviour(t *testing.T) {
	base := errors.New("boom")
	e := Wrap(CASConflict, "x", base)
	if !errors.Is(e, base) {
		t.Error("Unwrap chain broken")
	}
	if !errors.Is(e, New(CASConflict, "")) {
		t.Error("Is by code failed")
	}
	if errors.Is(e, New(NotFound, "")) {
		t.Error("Is matched different code")
	}
	wrapped := fmt.Errorf("outer: %w", e)
	if c, ok := CodeOf(wrapped); !ok || c != CASConflict {
		t.Errorf("CodeOf = %q, %v", c, ok)
	}
	if _, ok := CodeOf(base); ok {
		t.Error("CodeOf on plain error")
	}
	if _, ok := CodeOf(nil); ok {
		t.Error("CodeOf nil")
	}
	if got := New(NotFound, "m").Error(); got != "core.not_found: m" {
		t.Errorf("Error() = %q", got)
	}
	if got := Newf(NotFound, "id=%d", 3).Msg; got != "id=3" {
		t.Errorf("Newf = %q", got)
	}
}
