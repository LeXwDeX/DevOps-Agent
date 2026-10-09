package ids

import (
	"errors"
	"testing"

	"github.com/LeXwDeX/DevOps-Agent/platform/internal/schema/errcode"
)

func TestValueObjects(t *testing.T) {
	if FirstRevision != 1 || !FirstRevision.Valid() || Revision(0).Valid() || Revision(-1).Valid() {
		t.Error("revision validity")
	}
	if Revision(1).Next() != 2 || Version(7).Next() != 8 {
		t.Error("Next")
	}
	if CallID("").Valid() || !CallID("c1").Valid() {
		t.Error("CallID validity")
	}
}

func TestDigestValid(t *testing.T) {
	good, _ := DigestOf("x")
	if !good.Valid() || len(good) != 64 {
		t.Fatalf("digest %q", good)
	}
	for _, bad := range []string{"", "abc", string(good) + "0", "G" + string(good[1:]), "A" + string(good[1:])} {
		if Digest(bad).Valid() {
			t.Errorf("%q should be invalid", bad)
		}
		if _, err := ParseDigest(bad); !errors.Is(err, errcode.New(errcode.InvalidArgument, "")) {
			t.Errorf("ParseDigest(%q) err = %v", bad, err)
		}
	}
	if d, err := ParseDigest(string(good)); err != nil || d != good {
		t.Error("ParseDigest good")
	}
}
