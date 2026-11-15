package gameserver

// AI 角色状态：每个已接入的 Agent 都有一份「我在哪、在干什么、想钓什么」。
// 状态存在主库 ai_actor_state，与对话历史解耦——历史会被预算裁掉，状态不会，
// 所以聊再久也不会出现「上一句在这张图、下一句飘到别处」。
//
// 谁改状态：
//   - gameserver：AI 上鱼演出时把 loc/目标鱼对齐到它实际在的图；
//   - aibridge：玩家提到某鱼时，按低概率 + 冷却把目标鱼/地图切过去。
// 两边都不写对话内容，只改这一行。

import (
	"context"
	"math/rand"
	"time"

	"rf4server/internal/store"
)

// actorEnsure 取 Agent 状态；没有就按「当前地图 + 随机一个本图鱼」初始化。
// 初始活动不一定是钓鱼：也可能已经在休息或闲逛。
func (s *Server) actorEnsure(ctx context.Context, agentKey, loc string) *store.AIActor {
	if s.DB == nil || agentKey == "" {
		return nil
	}
	a, err := s.DB.AIGetActor(ctx, agentKey)
	if err == nil && a != nil {
		// 后台先打开开关时还没有地图/目标鱼；用随机地图补全，避免所有角色挤一张图。
		if (a.Loc == "" || a.TargetFish == "") && loc != "" {
			if a.Loc == "" {
				if k, n, mapErr := s.DB.AIRandomMap(ctx); mapErr == nil && k != "" {
					a.Loc, a.LocName = k, n
				} else {
					a.Loc, a.LocName = loc, s.actorLocName(ctx, loc)
				}
				a.LocSinceTS = float64(time.Now().Unix())
			}
			if a.TargetFish == "" {
				a.TargetFish, a.TargetName = s.actorPickFish(ctx, a.Loc)
			}
			_ = s.DB.AISaveActor(ctx, a)
		}
		return a
	}
	now := float64(time.Now().Unix())
	rng := rand.New(rand.NewSource(store.AIActorSeed(now, agentKey)))
	cfg := s.DB.AIActorConfig(ctx)
	// 新角色的地图随机分散，而不是统一放在玩家当前地图。
	startLoc, startName := loc, s.actorLocName(ctx, loc)
	if k, n, mapErr := s.DB.AIRandomMap(ctx); mapErr == nil && k != "" {
		startLoc, startName = k, n
	}
	a = store.AIActorStart(agentKey, startLoc, startName, now, cfg, rng.Float64)
	a.TargetFish, a.TargetName = s.actorPickFish(ctx, startLoc)
	if err := s.DB.AISaveActor(ctx, a); err != nil {
		s.log.Warn("AI 角色状态初始化失败", "agent", agentKey, "err", err)
		return nil
	}
	return a
}

// actorAdvance 把到期的活动推进到下一段；返回是否有改动。
func (s *Server) actorAdvance(ctx context.Context, a *store.AIActor) {
	if s.DB == nil || a == nil {
		return
	}
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	if !a.Advance(float64(time.Now().Unix()), s.DB.AIActorConfig(ctx), rng.Float64) {
		return
	}
	if err := s.DB.AISaveActor(ctx, a); err != nil {
		s.log.Warn("AI 角色状态推进失败", "agent", a.AgentKey, "err", err)
	}
}

// actorLocName 把 level key 换成中文水域名；查不到就原样返回。
func (s *Server) actorLocName(ctx context.Context, loc string) string {
	if loc == "" {
		return ""
	}
	var name string
	_ = s.DB.SQL.QueryRowContext(ctx,
		`SELECT loc FROM loc_alias WHERE alias=?`, loc).Scan(&name)
	if name != "" {
		return name
	}
	return loc
}

// actorPickFish 从本图鱼种里随机挑一条当目标（按样本量加权，常见的更容易被选中）。
func (s *Server) actorPickFish(ctx context.Context, loc string) (key, name string) {
	if s.DB == nil || loc == "" {
		return "", ""
	}
	rows, err := s.DB.SQL.QueryContext(ctx, `
		SELECT r.fish_key,
		       COALESCE(NULLIF(lf.name_zh,''), NULLIF(lw.fish_zh,''), ''),
		       COALESCE(g.catches,0)
		FROM map_fish_roster r
		LEFT JOIN loc_alias la ON la.alias=r.loc
		LEFT JOIN loc_fish lf ON lf.fish_key=r.fish_key AND lf.loc=la.loc
		LEFT JOIN loc_wiki_fish lw ON lw.fish_key=r.fish_key AND lw.loc=la.loc
		LEFT JOIN species_grade g ON g.fish_key=r.fish_key
		WHERE r.loc=?`, loc)
	if err != nil {
		return "", ""
	}
	defer rows.Close()
	type cand struct {
		key, name string
		w         int64
	}
	list := make([]cand, 0, 32)
	var total int64
	for rows.Next() {
		var c cand
		if rows.Scan(&c.key, &c.name, &c.w) != nil || c.key == "" {
			continue
		}
		if c.w < 1 {
			c.w = 1
		}
		if c.name == "" {
			c.name = c.key
		}
		list = append(list, c)
		total += c.w
	}
	if len(list) == 0 {
		return "", ""
	}
	pick := rand.Int63n(total)
	for _, c := range list {
		pick -= c.w
		if pick < 0 {
			return c.key, c.name
		}
	}
	return list[0].key, list[0].name
}

// actorNoteCatch 记一次 AI 上鱼：对齐地图、必要时把目标鱼换成同图另一条，
// 让「一直在钓同一条」不显得机械。
func (s *Server) actorNoteCatch(ctx context.Context, agentKey, loc, fishKey, fishName string) {
	if s.DB == nil || agentKey == "" {
		return
	}
	a := s.actorEnsure(ctx, agentKey, loc)
	if a == nil {
		return
	}
	now := float64(time.Now().Unix())
	changed := false
	if loc != "" && a.Loc != loc {
		a.Loc, a.LocName, a.LocSinceTS = loc, s.actorLocName(ctx, loc), now
		changed = true
	}
	// 钓到当前目标就换一条，否则保持。
	if fishKey == a.TargetFish {
		k, n := s.actorPickFish(ctx, a.Loc)
		if k != "" && k != a.TargetFish {
			a.TargetFish, a.TargetName, a.SinceTS = k, n, now
			changed = true
		}
	}
	if a.Activity != "fishing" {
		a.Activity, a.SinceTS = "fishing", now
		changed = true
	}
	if a.ActivityUntilTS <= now {
		cfg := s.DB.AIActorConfig(ctx)
		a.ActivityUntilTS = now + cfg.FishingMax // 上鱼后至少再钓一整段
		changed = true
	}
	if !changed {
		return
	}
	if err := s.DB.AISaveActor(ctx, a); err != nil {
		s.log.Warn("AI 角色状态更新失败", "agent", agentKey, "err", err)
	}
}
