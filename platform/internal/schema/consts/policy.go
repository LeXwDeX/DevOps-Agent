package consts

// PolicyMode 是策略模式（22 第 2.3、4 节）。allow_auto 是另外的布尔项。
type PolicyMode string

const (
	PolicyAuto   PolicyMode = "auto"
	PolicyManual PolicyMode = "manual"
	PolicyForbid PolicyMode = "forbid"
)

var policyModes = []PolicyMode{PolicyAuto, PolicyManual, PolicyForbid}

func PolicyModes() []PolicyMode { return append([]PolicyMode(nil), policyModes...) }

// All 返回该类型全部取值（有序），等同 PolicyModes()。
func (PolicyMode) All() []PolicyMode { return PolicyModes() }

func (m PolicyMode) Valid() bool { return contains(policyModes, m) }

func ParsePolicyMode(s string) (PolicyMode, error) { return parse("policy mode", policyModes, s) }

// PolicyLayer 是策略层级，顺序从细到粗（K-07）。
type PolicyLayer string

const (
	LayerAction PolicyLayer = "action"
	LayerZone   PolicyLayer = "zone"
	LayerGlobal PolicyLayer = "global"
	LayerSystem PolicyLayer = "system"
)

var policyLayers = []PolicyLayer{LayerAction, LayerZone, LayerGlobal, LayerSystem}

func PolicyLayers() []PolicyLayer { return append([]PolicyLayer(nil), policyLayers...) }

// All 返回该类型全部取值（有序），等同 PolicyLayers()。
func (PolicyLayer) All() []PolicyLayer { return PolicyLayers() }

func (l PolicyLayer) Valid() bool { return contains(policyLayers, l) }

func ParsePolicyLayer(s string) (PolicyLayer, error) { return parse("policy layer", policyLayers, s) }

// Zone 是权限区。
type Zone string

const (
	ZoneRead  Zone = "read"
	ZoneWrite Zone = "write"
)

var zones = []Zone{ZoneRead, ZoneWrite}

func Zones() []Zone { return append([]Zone(nil), zones...) }

// All 返回该类型全部取值（有序），等同 Zones()。
func (Zone) All() []Zone { return Zones() }

func (z Zone) Valid() bool { return contains(zones, z) }

func ParseZone(s string) (Zone, error) { return parse("zone", zones, s) }

// SessionMode 是会话模式（22 第 3 节）。
type SessionMode string

const (
	SessionManual SessionMode = "manual"
	SessionAuto   SessionMode = "auto"
)

var sessionModes = []SessionMode{SessionManual, SessionAuto}

func SessionModes() []SessionMode { return append([]SessionMode(nil), sessionModes...) }

// All 返回该类型全部取值（有序），等同 SessionModes()。
func (SessionMode) All() []SessionMode { return SessionModes() }

func (m SessionMode) Valid() bool { return contains(sessionModes, m) }

func ParseSessionMode(s string) (SessionMode, error) { return parse("session mode", sessionModes, s) }

// HumanAction 是人工动作（K-09）：▶ 执行、✕ 拒绝、点评。
type HumanAction string

const (
	ActionExecute HumanAction = "execute"
	ActionReject  HumanAction = "reject"
	ActionComment HumanAction = "comment"
)

var humanActions = []HumanAction{ActionExecute, ActionReject, ActionComment}

func HumanActions() []HumanAction { return append([]HumanAction(nil), humanActions...) }

// All 返回该类型全部取值（有序），等同 HumanActions()。
func (HumanAction) All() []HumanAction { return HumanActions() }

func (a HumanAction) Valid() bool { return contains(humanActions, a) }

func ParseHumanAction(s string) (HumanAction, error) { return parse("human action", humanActions, s) }

// SelfReviewVerdict 是自审结论。缺失或未完成由调用方按 risk 处理。
type SelfReviewVerdict string

const (
	VerdictNoRisk SelfReviewVerdict = "no_risk"
	VerdictRisk   SelfReviewVerdict = "risk"
)

var selfReviewVerdicts = []SelfReviewVerdict{VerdictNoRisk, VerdictRisk}

func SelfReviewVerdicts() []SelfReviewVerdict {
	return append([]SelfReviewVerdict(nil), selfReviewVerdicts...)
}

// All 返回该类型全部取值（有序），等同 SelfReviewVerdicts()。
func (SelfReviewVerdict) All() []SelfReviewVerdict { return SelfReviewVerdicts() }

func (v SelfReviewVerdict) Valid() bool { return contains(selfReviewVerdicts, v) }

func ParseSelfReviewVerdict(s string) (SelfReviewVerdict, error) {
	return parse("self review verdict", selfReviewVerdicts, s)
}
