package consts

import (
	"errors"
	"testing"

	"github.com/LeXwDeX/DevOps-Agent/platform/internal/schema/errcode"
)

// enumCase 把各枚举统一成字符串形式，便于表驱动测试。
type enumCase struct {
	name  string
	all   []string
	valid func(string) bool
	parse func(string) (string, error)
}

func strs[T ~string](in []T) []string {
	out := make([]string, len(in))
	for i, v := range in {
		out[i] = string(v)
	}
	return out
}

func mk[T ~string](name string, all []T, parse func(string) (T, error), valid func(T) bool) enumCase {
	return enumCase{
		name:  name,
		all:   strs(all),
		valid: func(s string) bool { return valid(T(s)) },
		parse: func(s string) (string, error) { v, err := parse(s); return string(v), err },
	}
}

func cases() []enumCase {
	return []enumCase{
		mk("RequestState", RequestState("").All(), ParseRequestState, RequestState.Valid),
		mk("RunState", RunState("").All(), ParseRunState, RunState.Valid),
		mk("PolicyMode", PolicyMode("").All(), ParsePolicyMode, PolicyMode.Valid),
		mk("PolicyLayer", PolicyLayer("").All(), ParsePolicyLayer, PolicyLayer.Valid),
		mk("Zone", Zone("").All(), ParseZone, Zone.Valid),
		mk("SessionMode", SessionMode("").All(), ParseSessionMode, SessionMode.Valid),
		mk("HumanAction", HumanAction("").All(), ParseHumanAction, HumanAction.Valid),
		mk("SelfReviewVerdict", SelfReviewVerdict("").All(), ParseSelfReviewVerdict, SelfReviewVerdict.Valid),
	}
}

func TestEnums(t *testing.T) {
	for _, c := range cases() {
		t.Run(c.name, func(t *testing.T) {
			if len(c.all) == 0 {
				t.Fatal("empty")
			}
			seen := map[string]bool{}
			for _, v := range c.all {
				if v == "" || seen[v] {
					t.Errorf("empty or duplicate %q", v)
				}
				seen[v] = true
				if !c.valid(v) {
					t.Errorf("%q not valid", v)
				}
				got, err := c.parse(v)
				if err != nil || got != v {
					t.Errorf("parse(%q) = %q, %v", v, got, err)
				}
			}
			for _, bad := range []string{"", "Auto", " auto", "AUTO", "unknown_value", "draft "} {
				if seen[bad] {
					continue
				}
				if c.valid(bad) {
					t.Errorf("%q should be invalid", bad)
				}
				got, err := c.parse(bad)
				if got != "" {
					t.Errorf("parse(%q) returned non-zero %q", bad, got)
				}
				if code, ok := errcode.CodeOf(err); !ok || code != errcode.InvalidArgument {
					t.Errorf("parse(%q) err = %v", bad, err)
				}
				if !errors.Is(err, errcode.New(errcode.InvalidArgument, "")) {
					t.Errorf("errors.Is failed for %q", bad)
				}
			}
		})
	}
}

func TestAllReturnsCopy(t *testing.T) {
	a := RequestState("").All()
	a[0] = "tampered"
	if RequestState("").All()[0] != RequestDraft {
		t.Error("All() exposes internal slice")
	}
}

func TestIsTerminal(t *testing.T) {
	reqTerm := map[RequestState]bool{
		RequestRejected: true, RequestSucceeded: true, RequestMismatched: true,
		RequestFailed: true, RequestUnknown: true, RequestStopped: true,
	}
	for _, s := range RequestStates() {
		if s.IsTerminal() != reqTerm[s] {
			t.Errorf("RequestState %q terminal = %v", s, s.IsTerminal())
		}
	}
	if RequestState("bogus").IsTerminal() {
		t.Error("unknown state must not be terminal")
	}
	runTerm := map[RunState]bool{
		RunCompleted: true, RunStopped: true, RunCancelled: true, RunRecoveryInterrupted: true,
	}
	for _, s := range RunStates() {
		if s.IsTerminal() != runTerm[s] {
			t.Errorf("RunState %q terminal = %v", s, s.IsTerminal())
		}
	}
}

func TestPolicyLayerOrder(t *testing.T) {
	want := []PolicyLayer{LayerAction, LayerZone, LayerGlobal, LayerSystem}
	got := PolicyLayers()
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("layer order = %v", got)
		}
	}
}
