package gameserver

// AI 联系人的查询封装。代码里不再保存任何 Agent 常量：
// 后台创建 Agent 并点「接入游戏」后才进入社交列表、才接收 19/1 私聊。

import (
	"context"
	"math/rand"
	"sort"
	"strings"
	"sync"

	"rf4server/internal/store"
)

func (s *Server) aiAgentByContactUID(ctx context.Context, uid int64) (*store.AIAgent, bool) {
	if s.DB == nil {
		return nil, false
	}
	a, err := s.DB.AIAgentByContactUID(ctx, uid)
	if err != nil || a == nil {
		return nil, false
	}
	return a, true
}

func (s *Server) aiJoinedAgents(ctx context.Context) []store.AIAgent {
	if s.DB == nil {
		return nil
	}
	list, err := s.DB.AIJoinedAgents(ctx)
	if err != nil {
		return nil
	}
	return list
}

func (s *Server) aiAllAgents(ctx context.Context) []store.AIAgent {
	if s.DB == nil {
		return nil
	}
	list, err := s.DB.AIListAgents(ctx)
	if err != nil {
		return nil
	}
	return list
}

func (s *Server) aiAgentByKey(ctx context.Context, key string) (*store.AIAgent, bool) {
	if s.DB == nil {
		return nil, false
	}
	a, err := s.DB.AIAgentByKey(ctx, key)
	if err != nil || a == nil {
		return nil, false
	}
	return a, true
}

// aiAgentByName 支持按展示名或 key 找 Agent（@邀请 / @移出 用）。
func (s *Server) aiAgentByName(ctx context.Context, name string) (*store.AIAgent, bool) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, false
	}
	for _, a := range s.aiAllAgents(ctx) {
		if a.Display == name || a.Key == name {
			return &a, true
		}
	}
	return nil, false
}

// aiAgentByMention 在已接入的 Agent 里按「@名字」或「直接叫名字」找。
func (s *Server) aiAgentByMention(ctx context.Context, msg string) (*store.AIAgent, bool) {
	if list := s.aiAgentMentions(ctx, msg); len(list) > 0 {
		return &list[0], true
	}
	return nil, false
}

// aiAgentHit 返回名字在消息里出现的位置，未出现返回 -1。
// 客户端不会为 AI 联系人弹 @ 补全，所以「直接叫名字」必须能生效；
// 「@名字」只是可选写法，命中位置按去掉 @ 后的名字算，保证顺序一致。
func aiAgentHit(msg string, a store.AIAgent) int {
	best := -1
	for _, name := range []string{a.Display, a.Key} {
		runes := len([]rune(name))
		if runes < 2 {
			continue // 单字名字太容易误伤普通聊天
		}
		// key 是对外可见的英文标识，2 字 key（如 "ai"）在英文句子里太容易误命中。
		if name == a.Key && runes < 3 {
			continue
		}
		if i := strings.Index(msg, name); i >= 0 && (best < 0 || i < best) {
			best = i
		}
	}
	return best
}

// aiAgentMentions 收集一条消息里叫到的全部已接入 Agent，按叫到的先后排序。
func (s *Server) aiAgentMentions(ctx context.Context, msg string) []store.AIAgent {
	type hit struct {
		pos int
		ag  store.AIAgent
	}
	hits := make([]hit, 0, 3)
	for _, a := range s.aiJoinedAgents(ctx) {
		if i := aiAgentHit(msg, a); i >= 0 {
			hits = append(hits, hit{pos: i, ag: a})
		}
	}
	sort.Slice(hits, func(i, j int) bool { return hits[i].pos < hits[j].pos })
	out := make([]store.AIAgent, 0, len(hits))
	for _, h := range hits {
		out = append(out, h.ag)
	}
	return out
}

func (s *Server) aiChannelAgents(ctx context.Context, channel string) []store.AIAgent {
	if s.DB == nil {
		return nil
	}
	list, err := s.DB.AIChannelAgents(ctx, channel)
	if err != nil {
		return nil
	}
	return list
}

// aiLastSpeaker 记录每个频道上一次回答的 Agent，避免连着两次都是同一个人。
var aiLastSpeaker sync.Map // channel -> string

// aiAgentCanChime 表示没人点名时它会不会主动接话。
// mention=只答点名，silent=只看不说，都不参与随机选人。
func aiAgentCanChime(a store.AIAgent) bool {
	return a.ReplyMode != "mention" && a.ReplyMode != "silent"
}

// aiDropSilent 去掉「只看不说」的 Agent；点名与统计都不应该带上它们。
func aiDropSilent(list []store.AIAgent) []store.AIAgent {
	out := make([]store.AIAgent, 0, len(list))
	for _, a := range list {
		if a.ReplyMode != "silent" {
			out = append(out, a)
		}
	}
	return out
}

// aiChimeAgents 只保留会主动接话的 Agent（用于群称与自发圆桌）。
func aiChimeAgents(list []store.AIAgent) []store.AIAgent {
	out := make([]store.AIAgent, 0, len(list))
	for _, a := range list {
		if aiAgentCanChime(a) {
			out = append(out, a)
		}
	}
	return out
}

// aiPickSpeaker 按活跃度随机挑一个频道内的 Agent 回答；频道里只有一个人时就是它。
// 没有「可主动接话」的 Agent 时返回 false——频道安静着也正常。
func (s *Server) aiPickSpeaker(ctx context.Context, channel string) (key string, contactUID int64, cooldownMS int, ok bool) {
	if s.DB == nil {
		return "", 0, 0, false
	}
	subscribed := s.aiChannelAgents(ctx, channel)
	if len(subscribed) == 0 {
		return "", 0, 0, false
	}
	last := ""
	if v, loaded := aiLastSpeaker.Load(channel); loaded {
		last, _ = v.(string)
	}
	cands := make([]store.AIAgent, 0, len(subscribed))
	for _, a := range subscribed {
		if aiAgentCanChime(a) {
			cands = append(cands, a)
		}
	}
	if len(cands) == 0 {
		return "", 0, 0, false
	}
	if len(cands) > 1 {
		others := make([]store.AIAgent, 0, len(cands))
		for _, a := range cands {
			if a.Key != last {
				others = append(others, a)
			}
		}
		if len(others) > 0 {
			cands = others
		}
	}
	total := 0
	for _, a := range cands {
		total += 10 + a.Talkativeness
	}
	pick := rand.Intn(total)
	a := cands[len(cands)-1]
	for _, c := range cands {
		pick -= 10 + c.Talkativeness
		if pick < 0 {
			a = c
			break
		}
	}
	aiLastSpeaker.Store(channel, a.Key)
	return a.Key, a.ContactUID, s.DB.AIChannelCooldownMS(ctx, channel), true
}

func (s *Server) aiAgentNames(ctx context.Context) string {
	names := make([]string, 0, 4)
	for _, a := range s.aiJoinedAgents(ctx) {
		names = append(names, a.Display)
	}
	if len(names) == 0 {
		return "（还没有 Agent 接入游戏，请先在后台添加并接入）"
	}
	return strings.Join(names, "、")
}
