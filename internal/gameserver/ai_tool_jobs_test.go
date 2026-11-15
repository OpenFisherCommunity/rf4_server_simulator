package gameserver

import "testing"

func TestPollAIToolJobsWritesResult(t *testing.T) {
	s, db := newProgressSrv(t)
	ctx := t.Context()
	id, err := db.AIEnqueueToolJob(ctx, 42, "fish_query", "常见拟鲤", 1000001)
	if err != nil {
		t.Fatalf("入队: %v", err)
	}
	s.pollAIToolJobs(ctx, db)
	got, ok, err := db.AIGetToolResult(ctx, id)
	if err != nil || !ok || got == "" {
		t.Fatalf("工具结果 = %q ok=%v err=%v", got, ok, err)
	}
	if pending, _ := db.AIPendingToolJobs(ctx, 8); len(pending) != 0 {
		t.Fatalf("处理完仍有待处理作业: %v", pending)
	}
}

func TestPollAIToolJobsUnknownToolStillCompletes(t *testing.T) {
	s, db := newProgressSrv(t)
	ctx := t.Context()
	id, err := db.AIEnqueueToolJob(ctx, 43, "not_a_tool", "x", 1000001)
	if err != nil {
		t.Fatal(err)
	}
	s.pollAIToolJobs(ctx, db)
	got, ok, _ := db.AIGetToolResult(ctx, id)
	if !ok || got != "未知工具" {
		t.Fatalf("未知工具应回固定文案: %q %v", got, ok)
	}
}
