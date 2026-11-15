package store

import "testing"

// 角色状态推进是纯函数：活动没到点就不动，到点才决定下一段。
func TestAIActorAdvanceWaitsUntilDue(t *testing.T) {
	a := &AIActor{Activity: "fishing", ActivityUntilTS: 100, SinceTS: 10, Energy: 80}
	cfg := AIActorConfig{FishingMin: 1000, FishingMax: 2000, RestMin: 500, RestMax: 800, RestChance: 1}
	if a.Advance(50, cfg, func() float64 { return 0 }) {
		t.Fatal("未到活动结束时间不应改状态")
	}
	if a.Activity != "fishing" {
		t.Fatalf("activity = %q, want fishing", a.Activity)
	}
}

func TestAIActorAdvanceFishingToRest(t *testing.T) {
	a := &AIActor{Activity: "fishing", ActivityUntilTS: 100, SinceTS: 10, Energy: 80}
	cfg := AIActorConfig{FishingMin: 1000, FishingMax: 2000, RestMin: 500, RestMax: 800, RestChance: 1}
	if !a.Advance(200, cfg, func() float64 { return 0 }) {
		t.Fatal("到点后应该推进状态")
	}
	if a.Activity != "resting" {
		t.Fatalf("activity = %q, want resting", a.Activity)
	}
	if a.SinceTS != 200 {
		t.Fatalf("since = %v, want 200", a.SinceTS)
	}
	if a.ActivityUntilTS != 700 {
		t.Fatalf("until = %v, want 700", a.ActivityUntilTS)
	}
}

func TestAIActorAdvanceLowEnergyForcesRest(t *testing.T) {
	a := &AIActor{Activity: "fishing", ActivityUntilTS: 100, Energy: 20}
	cfg := AIActorConfig{FishingMin: 1000, FishingMax: 2000, RestMin: 500, RestMax: 800, RestChance: 0}
	a.Advance(200, cfg, func() float64 { return 0.99 })
	if a.Activity != "resting" {
		t.Fatalf("体力低应该强制休息，activity = %q", a.Activity)
	}
}

func TestAIActorAdvanceRestToFishing(t *testing.T) {
	a := &AIActor{Activity: "resting", ActivityUntilTS: 100, Energy: 60}
	cfg := AIActorConfig{FishingMin: 1000, FishingMax: 2000, RestMin: 500, RestMax: 800, RestChance: 0.35}
	a.Advance(200, cfg, func() float64 { return 0.1 })
	if a.Activity != "fishing" {
		t.Fatalf("体力恢复后应该回到钓鱼，activity = %q", a.Activity)
	}
}

func TestAIActorStartCanBeginResting(t *testing.T) {
	cfg := AIActorConfig{FishingMin: 1000, FishingMax: 2000, RestMin: 500, RestMax: 800, RestChance: 0.35}
	a := AIActorStart("fishpal", "level_1", "测试湖", 100, cfg, func() float64 { return 0.1 })
	if a.Activity != "resting" {
		t.Fatalf("activity = %q, want resting", a.Activity)
	}
	if a.ActivityUntilTS != 630 {
		t.Fatalf("until = %v, want 630", a.ActivityUntilTS)
	}
}
