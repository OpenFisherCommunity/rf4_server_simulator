package gameserver

// 玩家自己上星/上蓝时，让同频道的 Agent 接一句。
// 与 AI 钓鱼演出是两条链路：那条是 AI 自己上鱼，这条是「看别人上鱼的反应」，
// 不产生任何鱼、不动经济，只往频道里加一句人话。

import (
	"context"
	"fmt"
	"math/rand"
	"time"
)

// aiCatchReactCooldown 防止玩家连上几条星鱼时 AI 每杆都接话。
const aiCatchReactCooldown = 30 * time.Second

func (s *Server) maybeAIPlayerCatch(ctx context.Context, uid int64, f *RolledFish) {
	if s.DB == nil || f == nil || f.Grade < 3 {
		return // 3=奖杯（星），4=蓝冠；达标与未达标不惊动 AI
	}
	if s.DB.AISetting(ctx, "ai_catch_talk", "1") != "1" {
		s.log.Info("AI 上星反应跳过", "uid", uid, "grade", f.Grade, "reason", "talk_off")
		return
	}
	if s.DB.AISetting(ctx, "emergency_stop", "0") == "1" ||
		s.DB.AISetting(ctx, "outbound_enabled", "1") != "1" {
		s.log.Info("AI 上星反应跳过", "uid", uid, "grade", f.Grade, "reason", "outbound_off")
		return
	}
	sess := s.liveSessionByUID(uid)
	if sess == nil {
		s.log.Info("AI 上星反应跳过", "uid", uid, "grade", f.Grade, "reason", "session_missing")
		return
	}
	channel := sessChannel(sess).Name
	agents := aiChimeAgents(s.aiAgentsForChannel(ctx, channel))
	if len(agents) == 0 {
		s.log.Info("AI 上星反应跳过", "uid", uid, "grade", f.Grade, "channel", channel, "reason", "no_channel_agents")
		return
	}
	cooldownKey := channel + "|catch"
	if v, ok := aiChannelCooldown.Load(cooldownKey); ok {
		if last, ok := v.(time.Time); ok && time.Since(last) < aiCatchReactCooldown {
			s.log.Info("AI 上星反应跳过", "uid", uid, "grade", f.Grade, "channel", channel,
				"reason", "cooldown", "remain_ms", (aiCatchReactCooldown - time.Since(last)).Milliseconds())
			return
		}
	}
	super := f.Grade >= 4
	chanceKey, def := "ai_catch_star_chance", 0.6
	if super {
		chanceKey, def = "ai_catch_super_chance", 0.9
	}
	name := f.Name
	if name == "" {
		name = s.speciesZhName(ctx, f.Species)
	}
	if name == "" {
		name = f.Species
	}
	grade := "上星了"
	if super {
		grade = "上蓝冠了"
	}
	chance := s.aiFloatSetting(ctx, chanceKey, def, 0, 1)
	roll := rand.Float64()
	if roll >= chance {
		s.log.Info("AI 上星反应跳过", "uid", uid, "grade", f.Grade, "channel", channel,
			"fish", name, "reason", "chance", "chance", chance, "roll", roll)
		return
	}
	text := fmt.Sprintf("（频道里的其他人看不见的提示）玩家刚上了一条 %.2fkg 的%s，%s。"+
		"你也在同一个频道钓鱼，作为旁观者接一句，别背数据、别像播报。",
		float64(f.WeightG)/1000, name, grade)

	// 蓝冠更容易引发围观：按概率改成三人圆桌。
	// 圆桌总开关关闭、冷却中或超出每小时上限时，退回单人接话，不绕过统一护栏。
	if super && len(agents) >= 3 &&
		s.DB.AISetting(ctx, "roundtable_enabled", "0") == "1" {
		cooldown := time.Duration(s.aiIntSetting(ctx, "roundtable_cooldown_ms", 120000, 30000, 600000)) * time.Millisecond
		hourly := s.aiIntSetting(ctx, "roundtable_hourly_limit", 3, 1, 10)
		if rand.Float64() <
			s.aiFloatSetting(ctx, "ai_catch_roundtable_chance", 0.5, 0, 1) &&
			rtAllow(channel, cooldown, hourly) {
			others := make([]string, 0, 2)
			for _, a := range agents {
				if len(others) < 2 {
					others = append(others, a.Key)
				}
			}
			if _, err := s.DB.AIEnqueueSystemRoundtable(ctx, channel, uid, text, others); err == nil {
				aiChannelCooldown.Store(cooldownKey, time.Now())
				s.log.Info("AI 上星反应入队", "uid", uid, "grade", f.Grade, "channel", channel,
					"fish", name, "kind", "roundtable", "agents", len(others))
				return
			}
		}
	}

	pick := agents[rand.Intn(len(agents))]
	if _, err := s.DB.AIEnqueueSystemChannel(ctx, channel, uid, text, pick.Key, pick.ContactUID); err != nil {
		s.log.Warn("玩家上鱼触发 AI 失败", "channel", channel, "agent", pick.Key, "err", err)
	} else {
		aiChannelCooldown.Store(cooldownKey, time.Now())
		s.log.Info("AI 上星反应入队", "uid", uid, "grade", f.Grade, "channel", channel,
			"fish", name, "kind", "single", "agent", pick.Key)
	}
}
