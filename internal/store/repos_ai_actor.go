package store

// AI 角色状态：每个 Agent 一行，记录「在哪张图、在干什么、想钓什么」。
// 它与对话历史解耦：历史会按预算裁剪，状态不会，所以聊多久都不会精神分裂。
//
// 状态不是固定标签，而是一段有始有终的活动：钓鱼 → 休息 → 再钓鱼。
// 推进只发生在这两处：gameserver 的钓鱼节拍、aibridge 注入状态之前。

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math/rand"
	"strconv"
)

// AIActor 是 Agent 的当前状态。
type AIActor struct {
	AgentKey        string
	Loc             string
	LocName         string
	Activity        string // fishing / resting / moving / idle
	TargetFish      string
	TargetName      string
	SinceTS         float64 // 当前活动开始时间
	LocSinceTS      float64
	SwitchTS        float64
	ActivityUntilTS float64 // 当前活动预计结束时间；到点后重新掷下一段
	Energy          float64 // 0-100，钓鱼消耗、休息恢复
	UpdatedTS       float64
}

// AIActorConfig 是活动时长的参数。后台可调，但都有上下限。
type AIActorConfig struct {
	FishingMin float64
	FishingMax float64
	RestMin    float64
	RestMax    float64
	RestChance float64 // 一段钓鱼结束后，直接去休息的概率
}

// AIActorConfig 从 ai_setting 读参数；缺失时用默认值。
func (d *DB) AIActorConfig(ctx context.Context) AIActorConfig {
	c := AIActorConfig{FishingMin: 1200, FishingMax: 4800, RestMin: 600, RestMax: 2400, RestChance: 0.35}
	if v := d.aiFloatSetting(ctx, "ai_actor_fishing_session_min_s", c.FishingMin); v > 0 {
		c.FishingMin = v
	}
	if v := d.aiFloatSetting(ctx, "ai_actor_fishing_session_max_s", c.FishingMax); v > 0 {
		c.FishingMax = v
	}
	if v := d.aiFloatSetting(ctx, "ai_actor_rest_min_s", c.RestMin); v > 0 {
		c.RestMin = v
	}
	if v := d.aiFloatSetting(ctx, "ai_actor_rest_max_s", c.RestMax); v > 0 {
		c.RestMax = v
	}
	c.RestChance = d.aiFloatSetting(ctx, "ai_actor_rest_chance", c.RestChance)
	if c.FishingMax < c.FishingMin {
		c.FishingMax = c.FishingMin
	}
	if c.RestMax < c.RestMin {
		c.RestMax = c.RestMin
	}
	if c.RestChance < 0 {
		c.RestChance = 0
	}
	if c.RestChance > 1 {
		c.RestChance = 1
	}
	return c
}

func (d *DB) aiFloatSetting(ctx context.Context, key string, def float64) float64 {
	raw := d.AISetting(ctx, key, "")
	if raw == "" {
		return def
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return def
	}
	return v
}

func (c AIActorConfig) fishingDur(roll func() float64) float64 {
	return c.FishingMin + roll()*(c.FishingMax-c.FishingMin)
}

func (c AIActorConfig) restDur(roll func() float64) float64 {
	return c.RestMin + roll()*(c.RestMax-c.RestMin)
}

// AIActorStart 初始化一个角色状态：多数时候在钓鱼，也可能已经在休息或闲逛。
func AIActorStart(agentKey, loc, locName string, now float64, cfg AIActorConfig, roll func() float64) *AIActor {
	a := &AIActor{
		AgentKey: agentKey, Loc: loc, LocName: locName,
		Activity: "fishing", Energy: 55 + roll()*35,
		SinceTS: now, LocSinceTS: now, ActivityUntilTS: now + cfg.fishingDur(roll),
	}
	switch r := roll(); {
	case r < 0.2:
		a.Activity = "resting"
		a.Energy = 30 + roll()*30
		a.ActivityUntilTS = now + cfg.restDur(roll)
	case r < 0.28:
		a.Activity = "idle"
		a.Energy = 60 + roll()*20
		a.ActivityUntilTS = now + cfg.restDur(roll)
	}
	return a
}

// AIRandomMap 从有鱼的地图里随机挑一张，优先挑还没有 AI 在钓的图。
// 用于新角色初始化：不能让所有 Agent 都挤在玩家当前地图。
func (d *DB) AIRandomMap(ctx context.Context) (loc, name string, err error) {
	rows, err := d.SQL.QueryContext(ctx, `
		SELECT loc, COUNT(*) FROM map_fish_roster
		WHERE loc<>'' GROUP BY loc`)
	if err != nil {
		return "", "", err
	}
	type cand struct {
		key string
		w   int64
	}
	list := make([]cand, 0, 32)
	for rows.Next() {
		var key string
		var w int64
		if rows.Scan(&key, &w) == nil && key != "" {
			if w < 1 {
				w = 1
			}
			list = append(list, cand{key, w})
		}
	}
	_ = rows.Close()
	if len(list) == 0 {
		return "", "", nil
	}

	used := map[string]bool{}
	if urows, uerr := d.SQL.QueryContext(ctx,
		`SELECT DISTINCT loc FROM ai_actor_state WHERE loc<>''`); uerr == nil {
		for urows.Next() {
			var l string
			if urows.Scan(&l) == nil {
				used[l] = true
			}
		}
		_ = urows.Close()
	}
	free := make([]cand, 0, len(list))
	for _, c := range list {
		if !used[c.key] {
			free = append(free, c)
		}
	}
	if len(free) > 0 {
		list = free
	}

	total := int64(0)
	for _, c := range list {
		total += c.w
	}
	pick := rand.Int63n(total)
	loc = list[len(list)-1].key
	for _, c := range list {
		pick -= c.w
		if pick < 0 {
			loc = c.key
			break
		}
	}
	_ = d.SQL.QueryRowContext(ctx,
		`SELECT loc FROM loc_alias WHERE alias=? LIMIT 1`, loc).Scan(&name)
	if name == "" {
		name = loc
	}
	return loc, name, nil
}

// AIActorSeed 给角色状态掷骰用：同一秒给多个 Agent 初始化，也要有不同序列。
func AIActorSeed(now float64, agentKey string) int64 {
	h := int64(0)
	for _, r := range agentKey {
		h = h*131 + int64(r)
	}
	return int64(now)*31 + h
}

// AIActorReroll 立即换一个与当前不同的活动，保留地图与目标鱼。
// 连续点「换个状态」时不能因为随机到同一个值而看起来没反应。
func AIActorReroll(a *AIActor, now float64, cfg AIActorConfig, roll func() float64) {
	if a == nil {
		return
	}
	old := a.Activity
	for i := 0; i < 8; i++ {
		n := AIActorStart(a.AgentKey, a.Loc, a.LocName, now, cfg, roll)
		if n.Activity != old {
			a.Activity, a.SinceTS = n.Activity, n.SinceTS
			a.ActivityUntilTS, a.Energy = n.ActivityUntilTS, n.Energy
			return
		}
	}
	// 兜底：在钓鱼与休息之间切换。
	if old == "fishing" {
		a.Activity = "resting"
		a.Energy = clampF(a.Energy+15, 0, 100)
		a.ActivityUntilTS = now + cfg.restDur(roll)
	} else {
		a.Activity = "fishing"
		a.ActivityUntilTS = now + cfg.fishingDur(roll)
	}
	a.SinceTS = now
}

// Advance 在活动到期时把状态推到下一段；返回是否有改动。
// 没到点、或本来就没有状态时返回 false。
func (a *AIActor) Advance(now float64, cfg AIActorConfig, roll func() float64) bool {
	if a == nil {
		return false
	}
	if a.Activity == "" {
		a.Activity = "fishing"
	}
	if a.ActivityUntilTS <= 0 {
		// 旧数据没有到期时间：按当前活动补一段，不立刻改活动。
		if a.Activity == "resting" {
			a.ActivityUntilTS = now + cfg.restDur(roll)
		} else if a.Activity == "fishing" {
			a.ActivityUntilTS = now + cfg.fishingDur(roll)
		} else {
			a.ActivityUntilTS = now + cfg.restDur(roll)
		}
		return true
	}
	if now < a.ActivityUntilTS {
		return false
	}
	switch a.Activity {
	case "fishing":
		a.Energy = clampF(a.Energy-12, 0, 100)
		if a.Energy < 25 || roll() < cfg.RestChance {
			a.Activity = "resting"
			a.SinceTS = now
			a.Energy = clampF(a.Energy+20, 0, 100)
			a.ActivityUntilTS = now + cfg.restDur(roll)
		} else {
			// 继续钓，不重置 SinceTS：玩家问「蹲多久了」应该继续累加。
			a.ActivityUntilTS = now + cfg.fishingDur(roll)
		}
	case "resting":
		a.Energy = clampF(a.Energy+25, 0, 100)
		if a.Energy >= 65 || roll() < 0.5 {
			a.Activity = "fishing"
			a.SinceTS = now
			a.ActivityUntilTS = now + cfg.fishingDur(roll)
		} else {
			a.ActivityUntilTS = now + cfg.restDur(roll)
		}
	default: // idle / moving
		if roll() < 0.7 {
			a.Activity = "fishing"
		} else {
			a.Activity = "resting"
			a.Energy = clampF(a.Energy+10, 0, 100)
		}
		a.SinceTS = now
		a.ActivityUntilTS = now + cfg.fishingDur(roll)
	}
	return true
}

func clampF(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// AIGetActor 读一个 Agent 的状态；没有记录时返回 nil, nil。
func (d *DB) AIGetActor(ctx context.Context, agentKey string) (*AIActor, error) {
	var a AIActor
	err := d.SQL.QueryRowContext(ctx, `
		SELECT agent_key, loc, loc_name, activity, target_fish, target_name,
		       since_ts, loc_since_ts, switch_ts, activity_until_ts, energy, updated_ts
		FROM ai_actor_state WHERE agent_key=?`, agentKey).
		Scan(&a.AgentKey, &a.Loc, &a.LocName, &a.Activity, &a.TargetFish, &a.TargetName,
			&a.SinceTS, &a.LocSinceTS, &a.SwitchTS, &a.ActivityUntilTS, &a.Energy, &a.UpdatedTS)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &a, nil
}

// AISaveActor 覆盖写入一个 Agent 的状态。
func (d *DB) AISaveActor(ctx context.Context, a *AIActor) error {
	if a == nil || a.AgentKey == "" {
		return errors.New("ai actor: 缺少 agent_key")
	}
	a.UpdatedTS = nowTS()
	_, err := d.SQL.ExecContext(ctx, `
		INSERT INTO ai_actor_state(agent_key, loc, loc_name, activity, target_fish, target_name,
		                           since_ts, loc_since_ts, switch_ts, activity_until_ts, energy, updated_ts)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(agent_key) DO UPDATE SET
		  loc=excluded.loc, loc_name=excluded.loc_name, activity=excluded.activity,
		  target_fish=excluded.target_fish, target_name=excluded.target_name,
		  since_ts=excluded.since_ts, loc_since_ts=excluded.loc_since_ts,
		  switch_ts=excluded.switch_ts, activity_until_ts=excluded.activity_until_ts,
		  energy=excluded.energy, updated_ts=excluded.updated_ts`,
		a.AgentKey, a.Loc, a.LocName, a.Activity, a.TargetFish, a.TargetName,
		a.SinceTS, a.LocSinceTS, a.SwitchTS, a.ActivityUntilTS, a.Energy, a.UpdatedTS)
	if err != nil {
		return fmt.Errorf("ai actor save: %w", err)
	}
	return nil
}

// AIDeleteActor 删除一个 Agent 的角色状态（关掉「在钓鱼」时调用）。
func (d *DB) AIDeleteActor(ctx context.Context, agentKey string) error {
	_, err := d.SQL.ExecContext(ctx, `DELETE FROM ai_actor_state WHERE agent_key=?`, agentKey)
	return err
}

// AIListActors 读全部状态（后台展示「现在都在干嘛」用）。
func (d *DB) AIListActors(ctx context.Context) ([]AIActor, error) {
	rows, err := d.SQL.QueryContext(ctx, `
		SELECT agent_key, loc, loc_name, activity, target_fish, target_name,
		       since_ts, loc_since_ts, switch_ts, activity_until_ts, energy, updated_ts
		FROM ai_actor_state ORDER BY agent_key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AIActor
	for rows.Next() {
		var a AIActor
		if err := rows.Scan(&a.AgentKey, &a.Loc, &a.LocName, &a.Activity, &a.TargetFish,
			&a.TargetName, &a.SinceTS, &a.LocSinceTS, &a.SwitchTS, &a.ActivityUntilTS,
			&a.Energy, &a.UpdatedTS); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
