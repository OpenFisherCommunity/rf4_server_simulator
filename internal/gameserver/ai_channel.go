package gameserver

// 频道订阅与多人触发：哪个频道需要哪个 Agent，就把它拉进来。
// 防循环：AI 回复只经 24/27 推给客户端，永远不会变成 24/10 再触发一轮。

import (
	"context"
	"encoding/json"
	"math/rand"
	"strconv"
	"strings"
	"sync"
	"time"

	"rf4server/internal/store"
)

var aiChannelCooldown sync.Map // channel|agent -> time.Time

// Agent 名称解析见 ai_contacts.go：查库，不硬编码。
// 默认零接入时，这里不会命中任何 Agent。

// handleAIChannelCommand 处理 @邀请 / @移出。命中返回 true 并吞掉该消息。
// 不能用 "/" 前缀：hSocialPublic 会把斜杠开头的消息当 GM 指令丢掉。
func (s *Server) handleAIChannelCommand(ctx *Ctx, msg, channel string) bool {
	if s.DB == nil {
		return false
	}
	enable := false
	var rest string
	switch {
	case strings.HasPrefix(msg, "@邀请"):
		enable = true
		rest = strings.TrimSpace(strings.TrimPrefix(msg, "@邀请"))
	case strings.HasPrefix(msg, "@移出"):
		rest = strings.TrimSpace(strings.TrimPrefix(msg, "@移出"))
	default:
		return false
	}
	cctx := ctx.Sess.pushCtx()
	text := "未找到这个助手。已接入：" + s.aiAgentNames(cctx)
	if a, ok := s.aiAgentByName(cctx, rest); ok {
		if err := s.DB.AISetChannelAgent(cctx, channel, a.Key, enable); err != nil {
			s.log.Warn("频道订阅更新失败", "channel", channel, "agent", a.Key, "err", err)
			text = "操作失败，请稍后再试"
		} else if enable {
			text = "已把 " + a.Display + " 拉进本频道"
		} else {
			text = "已把 " + a.Display + " 移出本频道"
		}
	}
	ctx.Sess.Push(24, 27, buildChatEchoPush("", text, uint64(ctx.Sess.Userid), 1))
	return true
}

// 频道圆桌的冷却与每小时计数（进程内，重启清零可接受）。
type rtWindow struct {
	start time.Time
	count int
}

var rtStateByChannel = struct {
	sync.Mutex
	last map[string]time.Time
	hour map[string]rtWindow
}{last: map[string]time.Time{}, hour: map[string]rtWindow{}}

func rtAllow(channel string, cooldown time.Duration, hourly int) bool {
	rtStateByChannel.Lock()
	defer rtStateByChannel.Unlock()
	now := time.Now()
	if t, ok := rtStateByChannel.last[channel]; ok && now.Sub(t) < cooldown {
		return false
	}
	w := rtStateByChannel.hour[channel]
	if w.start.IsZero() || now.Sub(w.start) > time.Hour {
		w = rtWindow{start: now}
	}
	if w.count >= hourly {
		rtStateByChannel.hour[channel] = w
		return false
	}
	w.count++
	rtStateByChannel.last[channel] = now
	rtStateByChannel.hour[channel] = w
	return true
}

// aiIntSetting 读整数设置并夹在硬上限内（后台只能调小，不能突破代码上限）。
func (s *Server) aiIntSetting(ctx context.Context, key string, def, lo, hi int) int {
	v, err := strconv.Atoi(s.DB.AISetting(ctx, key, strconv.Itoa(def)))
	if err != nil {
		v = def
	}
	if v < lo {
		v = lo
	}
	if v > hi {
		v = hi
	}
	return v
}

// aiGroupCallWords 是「想让所有人一起说」的说法。命中就当圆桌，
// 不用点名——但依然受冷却与每小时上限约束，不会变成刷屏开关。
var aiGroupCallWords = []string{
	"你们", "大家", "各位", "大伙", "都来说", "都说说", "一起聊", "谁能告诉", "谁来说",
}

func aiGroupCall(msg string) bool {
	for _, w := range aiGroupCallWords {
		if strings.Contains(msg, w) {
			return true
		}
	}
	return false
}

// maybeTriggerChannelAI 决定一次真人发言要不要惊动 AI：
//  1. 叫到 ≥2 个助手 → 圆桌（点名优先，不受概率限制）
//  2. 没叫人但频道里有 ≥2 个助手 → 按 roundtable_on_speech_chance 概率起圆桌
//  3. 其余情况 → 单人回复；被点名的优先，没人叫就随机挑一个
//
// 频道的「热闹」主要交给钓鱼事件（见 ai_fishing.go），这里只在有人说话时接话。
// 不推任何「本频道有哪些助手」的公告：客户端本来就会在每条消息前显示说话人的名字，
// 让角色自己在第一次对话时顺口带一句，比系统提示自然。
func (s *Server) maybeTriggerChannelAI(ctx *Ctx, channel, msg string) {
	if s.DB == nil {
		return
	}
	cctx := ctx.Sess.pushCtx()
	if s.DB.AISetting(cctx, "emergency_stop", "0") == "1" {
		return
	}
	if strings.TrimSpace(msg) == "" {
		return
	}
	rtOn := s.DB.AISetting(cctx, "roundtable_enabled", "0") == "1"
	mentions := aiDropSilent(s.aiAgentMentions(cctx, msg))

	if rtOn && len(mentions) >= 2 {
		maxSpeakers := s.aiIntSetting(cctx, "roundtable_max_speakers", 3, 2, 3)
		if len(mentions) > maxSpeakers {
			mentions = mentions[:maxSpeakers]
		}
		if s.startChannelRoundtable(ctx, channel, msg, mentions, cctx) {
			return
		}
		// 冷却/超次数：退回单个 Agent 回复，避免完全不响应。
	}

	// 说了「你们」「大家」这类群称：直接开一轮圆桌，不用逐个点名。
	if rtOn && len(mentions) == 0 && aiGroupCall(msg) {
		subscribed := aiChimeAgents(s.aiChannelAgents(cctx, channel))
		if len(subscribed) >= 2 {
			if s.startChannelRoundtable(ctx, channel, msg, subscribed, cctx) {
				return
			}
		}
	}

	// 没叫人、但频道里不止一个助手：按概率自发聊一轮。
	// 概率受冷却与每小时上限双重约束，跟自由圆桌（必定触发）不是一回事。
	if rtOn && len(mentions) == 0 {
		subscribed := aiChimeAgents(s.aiChannelAgents(cctx, channel))
		chance := s.aiFloatSetting(cctx, "roundtable_on_speech_chance", 0.2, 0, 1)
		if len(subscribed) >= 2 && chance > 0 {
			rng := rand.New(rand.NewSource(time.Now().UnixNano()))
			if rng.Float64() < chance {
				if s.startChannelRoundtable(ctx, channel, msg, subscribed, cctx) {
					return
				}
			}
		}
	}

	// 单人回复：被点名的优先；没人叫就随机挑一个，避免总是同一个人答。
	key, contactUID, cooldownMS, ok := "", int64(0), 8000, false
	if len(mentions) > 0 {
		key, contactUID = mentions[0].Key, mentions[0].ContactUID
		cooldownMS = s.DB.AIChannelCooldownMS(cctx, channel)
		ok = true
	} else {
		key, contactUID, cooldownMS, ok = s.aiPickSpeaker(cctx, channel)
	}
	if !ok {
		return
	}
	ck := channel + "|" + key
	if v, ok := aiChannelCooldown.Load(ck); ok {
		if last, ok := v.(time.Time); ok && time.Since(last) < time.Duration(cooldownMS)*time.Millisecond {
			return
		}
	}
	aiChannelCooldown.Store(ck, time.Now())
	if _, err := s.DB.AIEnqueue(cctx, &store.AITask{
		Kind: "channel", Scope: "channel", ScopeKey: channel,
		UID: ctx.Sess.Userid, Agent: key, ContactUID: contactUID,
		SourceKind: "human", Text: msg,
		Loc:        s.currentLevel(ctx.Sess.Userid),
		DeadlineTS: float64(time.Now().Add(aiTaskTTL).Unix()),
	}); err != nil {
		s.log.Warn("频道 AI 任务入队失败", "channel", channel, "agent", key, "err", err)
	}
}

// startChannelRoundtable 在冷却与小时配额允许时入队一次圆桌，返回是否已入队。
// 参与者按「叫到的先后」取前 N 个；没点名时由调用方传入频道内的候选。
func (s *Server) startChannelRoundtable(ctx *Ctx, channel, msg string, speakers []store.AIAgent, cctx context.Context) bool {
	speakers = aiDropSilent(speakers)
	maxSpeakers := s.aiIntSetting(cctx, "roundtable_max_speakers", 3, 2, 3)
	if len(speakers) > maxSpeakers {
		speakers = speakers[:maxSpeakers]
	}
	if len(speakers) < 2 {
		return false
	}
	maxMS := s.aiIntSetting(cctx, "roundtable_max_ms", 20000, 5000, 30000)
	cooldownMS := s.aiIntSetting(cctx, "roundtable_cooldown_ms", 120000, 30000, 600000)
	hourly := s.aiIntSetting(cctx, "roundtable_hourly_limit", 3, 1, 10)
	if !rtAllow(channel, time.Duration(cooldownMS)*time.Millisecond, hourly) {
		return false
	}
	keys := make([]string, 0, len(speakers))
	for _, a := range speakers {
		keys = append(keys, a.Key)
	}
	parts, _ := json.Marshal(keys)
	if _, err := s.DB.AIEnqueue(cctx, &store.AITask{
		Kind: "roundtable", Scope: "channel", ScopeKey: channel,
		UID: ctx.Sess.Userid, SourceKind: "human", Text: msg,
		Participants: string(parts),
		Loc:          s.currentLevel(ctx.Sess.Userid),
		DeadlineTS:   float64(time.Now().Add(time.Duration(maxMS+5000) * time.Millisecond).Unix()),
	}); err != nil {
		s.log.Warn("圆桌任务入队失败", "channel", channel, "err", err)
		return false
	}
	return true
}

// deliverAIChannel 把 AI 回复按频道推给同频道在线会话。
// 外层 locationId 必须空串，客户端才不会静默丢弃。
func (s *Server) deliverAIChannel(r store.AIResult) bool {
	if r.ScopeKey == "" || r.ContactUID == 0 {
		return false
	}
	s.mu.Lock()
	peers := make([]*Session, 0, 8)
	for _, sess := range s.sessions {
		if sess != nil && !sess.Closed() && sessChannel(sess).Name == r.ScopeKey {
			peers = append(peers, sess)
		}
	}
	s.mu.Unlock()
	if len(peers) == 0 {
		return true // 频道已空，结果无需再投递
	}
	n := uint32(len(peers))
	for _, sess := range peers {
		sess.Push(24, 27, buildChatEchoPush("", r.ReplyText, uint64(r.ContactUID), n))
	}
	return true
}
