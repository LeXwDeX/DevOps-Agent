package policy

import (
	"testing"

	"github.com/LeXwDeX/DevOps-Agent/platform/internal/schema/consts"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/schema/errcode"
)

func TestRuleValidate(t *testing.T) {
	auto := ModePtr(consts.PolicyAuto)
	bad := consts.PolicyMode("bad")
	cases := []struct {
		name string
		rule Rule
		ok   bool
	}{
		{"action ok", Rule{Layer: consts.LayerAction, Subject: "ssh", Action: "restart", Mode: auto}, true},
		{"action no action", Rule{Layer: consts.LayerAction, Subject: "ssh", Mode: auto}, false},
		{"action no subject", Rule{Layer: consts.LayerAction, Action: "x", Mode: auto}, false},
		{"action with zone", Rule{Layer: consts.LayerAction, Subject: "s", Action: "x", Zone: consts.ZoneRead}, false},
		{"zone ok", Rule{Layer: consts.LayerZone, Subject: "ssh", Zone: consts.ZoneRead, Mode: auto, Environment: "prod"}, true},
		{"zone missing zone", Rule{Layer: consts.LayerZone, Subject: "ssh"}, false},
		{"zone bad zone", Rule{Layer: consts.LayerZone, Subject: "ssh", Zone: "x"}, false},
		{"zone with action", Rule{Layer: consts.LayerZone, Subject: "s", Zone: consts.ZoneRead, Action: "a"}, false},
		{"global ok", Rule{Layer: consts.LayerGlobal, Subject: "ssh", AllowAuto: BoolPtr(false)}, true},
		{"global with zone", Rule{Layer: consts.LayerGlobal, Subject: "ssh", Zone: consts.ZoneRead}, false},
		{"global no subject", Rule{Layer: consts.LayerGlobal, Mode: auto}, false},
		{"system mode", Rule{Layer: consts.LayerSystem, Mode: ModePtr(consts.PolicyManual)}, true},
		{"system allow_auto only", Rule{Layer: consts.LayerSystem, AllowAuto: BoolPtr(false)}, true},
		{"system empty", Rule{Layer: consts.LayerSystem}, false},
		{"system with subject", Rule{Layer: consts.LayerSystem, Subject: "x", Mode: auto}, false},
		{"bad layer", Rule{Layer: "x", Mode: auto}, false},
		{"bad mode", Rule{Layer: consts.LayerGlobal, Subject: "s", Mode: &bad}, false},
	}
	for _, c := range cases {
		err := c.rule.Validate()
		if (err == nil) != c.ok {
			t.Errorf("%s: err = %v", c.name, err)
		}
		if err != nil {
			if code, _ := errcode.CodeOf(err); code != errcode.InvalidArgument {
				t.Errorf("%s: code = %q", c.name, code)
			}
		}
	}
}

func TestSetValidate(t *testing.T) {
	good := Set{Version: "v1", Rules: []Rule{{Layer: consts.LayerSystem, Mode: ModePtr(consts.PolicyManual)}}}
	if err := good.Validate(); err != nil {
		t.Fatal(err)
	}
	badSet := Set{Rules: append(good.Rules, Rule{Layer: consts.LayerSystem})}
	if err := badSet.Validate(); err == nil {
		t.Fatal("want error")
	}
	if err := (Set{}).Validate(); err != nil {
		t.Fatalf("empty set should validate: %v", err)
	}
}

func TestDecisionClone(t *testing.T) {
	d := Decision{Reasons: []string{"a"}}
	c := d.Clone()
	c.Reasons[0] = "b"
	if d.Reasons[0] != "a" {
		t.Fatal("aliasing")
	}
}
