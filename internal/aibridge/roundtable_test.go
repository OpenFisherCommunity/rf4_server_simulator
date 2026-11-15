package aibridge

import (
	"math/rand"
	"testing"

	"rf4server/internal/store"
)

func rngStub() *rand.Rand { return rand.New(rand.NewSource(1)) }

func TestSimilarText(t *testing.T) {
	if !similarText("今天鱼口不错，可以去试试", "今天鱼口不错，可以去试试") {
		t.Fatal("完全相同应判复读")
	}
	if !similarText("河鲈在浅水区比较多，用亮片试试", "河鲈在浅水区比较多，用亮片也行") {
		t.Fatal("前 12 字相同应判复读")
	}
	if similarText("河鲈在浅水区", "鲤鱼在深水区") {
		t.Fatal("不同内容不应判复读")
	}
	if !similarText("好", "好") {
		t.Fatal("完全相同的短回复也算复读")
	}
	if similarText("好", "行") {
		t.Fatal("不同短回复不应判复读")
	}
}

func TestOtherNames(t *testing.T) {
	agents := []*store.AIAgent{
		{Key: "a", Display: "甲"},
		{Key: "b", Display: "乙"},
		{Key: "c", Display: "丙"},
	}
	if got := otherNames(agents, "a"); got != "乙、丙" {
		t.Fatalf("otherNames = %q", got)
	}
	if got := otherNames(agents[:1], "a"); got != "其他助手" {
		t.Fatalf("单人时应回兜底: %q", got)
	}
}

// AI 自己决定说不说：命中沉默标记就不投递，也不占条数。
func TestIsRoundtableStop(t *testing.T) {
	for _, s := range []string{"散了", "散了。", " 散了 ", "散了散了", "不语", "[不语]", "【不语】", "跳过", "PASS"} {
		if !isRoundtableStop(s) {
			t.Fatalf("%q 应判定为终止", s)
		}
	}
	for _, s := range []string{"", "确实。", "那就散了吧，我去换饵", "这轮聊得挺散的了，不过我还想说说昨天的鱼", "不是不语，是想说"} {
		if isRoundtableStop(s) {
			t.Fatalf("%q 不该判定为终止", s)
		}
	}
}

func TestRTMentionsOrder(t *testing.T) {
	agents := []*store.AIAgent{
		{Key: "a", Display: "甲"},
		{Key: "b", Display: "乙"},
		{Key: "c", Display: "丙"},
	}
	got := rtMentions("@丙 你刚说啥，@甲 也看看", agents)
	if len(got) != 2 || got[0].Key != "c" || got[1].Key != "a" {
		t.Fatalf("点名顺序不对: %+v", got)
	}
	if len(rtMentions("今天没叫人", agents)) != 0 {
		t.Fatal("没点名不应返回候选")
	}
}

func TestRTChancePrefersActiveAgents(t *testing.T) {
	quiet := &store.AIAgent{Key: "q", Talkativeness: 10}
	active := &store.AIAgent{Key: "a", Talkativeness: 90}
	st := newRTSpeakerState()
	if rtChance(quiet, st, false, 0.55) >= rtChance(active, st, false, 0.55) {
		t.Fatal("活跃度高的应该更容易开口")
	}
	st.last = "a"
	if rtChance(active, st, false, 0.55) != 0 {
		t.Fatal("刚说完的人不应连续再说")
	}
	// 真人刚说完的第一轮，至少给一个较高的基本概率。
	if rtChance(quiet, st, true, 0.1) < 0.1 {
		t.Fatal("首轮概率不应被压没")
	}
}

func TestRTPickSpeakerOnlySocialVolunteers(t *testing.T) {
	agents := []*store.AIAgent{
		{Key: "a", Display: "甲", ReplyMode: "mention"},
		{Key: "b", Display: "乙", ReplyMode: "social", Talkativeness: 100},
	}
	st := newRTSpeakerState()
	got := rtPickSpeaker(agents, st, map[string]bool{}, rtConfig{maxStreak: 3, chime: 1}, false, rngStub())
	if got == nil || got.Key != "b" {
		t.Fatalf("只有 social 会主动接话，got=%+v", got)
	}
}

func TestRTPickSpeakerHonorsMention(t *testing.T) {
	agents := []*store.AIAgent{
		{Key: "a", Display: "甲", ReplyMode: "mention"},
		{Key: "b", Display: "乙", ReplyMode: "social"},
	}
	st := newRTSpeakerState()
	st.pending = rtMentions("@甲 你看呢", agents)
	got := rtPickSpeaker(agents, st, map[string]bool{}, rtConfig{maxStreak: 3, chime: 0}, false, rngStub())
	if got == nil || got.Key != "a" {
		t.Fatalf("被点名的 mention-only Agent 应该开口，got=%+v", got)
	}
}

// 频道历史里带【名字】前缀，模型偶尔会照抄，回复前要剥掉。
func TestStripSpeakerPrefix(t *testing.T) {
	if got := stripSpeakerPrefix("【Agent 1】星了！"); got != "星了！" {
		t.Fatalf("前缀未剥离: %q", got)
	}
	if got := stripSpeakerPrefix("星了！"); got != "星了！" {
		t.Fatalf("无前缀不应改动: %q", got)
	}
	if got := stripSpeakerPrefix("【这不是名字前缀，是一句很长很长的话】"); got == "" {
		t.Fatal("过长的括号不应被当成前缀整段吃掉")
	}
}

// 频道共享一份上下文，私聊按 Agent 分开。
func TestSessionKeySharedForChannel(t *testing.T) {
	ch := &store.AITask{Kind: "channel", Scope: "channel", ScopeKey: "nav1_03", Agent: "hutao"}
	other := &store.AITask{Kind: "channel", Scope: "channel", ScopeKey: "nav1_03", Agent: "furina"}
	if sessionKey(ch) != sessionKey(other) {
		t.Fatal("同一频道的不同 Agent 应共用一份上下文")
	}
	dm1 := &store.AITask{Kind: "dm", Scope: "uid", ScopeKey: "1000001", Agent: "hutao"}
	dm2 := &store.AITask{Kind: "dm", Scope: "uid", ScopeKey: "1000001", Agent: "furina"}
	if sessionKey(dm1) == sessionKey(dm2) {
		t.Fatal("私聊必须按 Agent 隔离")
	}
	rt := &store.AITask{Kind: "roundtable", Scope: "channel", ScopeKey: "nav1_03"}
	if sessionKey(rt) != sessionKey(ch) {
		t.Fatal("圆桌应与频道共用上下文")
	}
}
