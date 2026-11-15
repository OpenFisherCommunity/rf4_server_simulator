package aibridge

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"rf4server/internal/store"
)

func TestMemoryLine(t *testing.T) {
	d, err := store.Open(filepath.Join(t.TempDir(), "mem.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if err := d.Migrate(); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := d.AIAddMemory(ctx, "fishguide", "star", "perch", "鲈鱼", 1200, "lake"); err != nil {
		t.Fatal(err)
	}
	if err := d.AIAddMemory(ctx, "fishguide", "super", "pike", "狗鱼", 4800, "lake"); err != nil {
		t.Fatal(err)
	}
	r := &Runner{db: d}
	line := r.memoryLine(ctx, "fishguide")
	for _, want := range []string{"蓝冠", "4.80kg", "狗鱼", "星", "1.20kg", "鲈鱼"} {
		if !strings.Contains(line, want) {
			t.Fatalf("记忆缺 %q: %s", want, line)
		}
	}
	if len([]rune(line)) > 150 {
		t.Fatalf("记忆超长: %d", len([]rune(line)))
	}
	if got := r.memoryLine(ctx, "nobody"); got != "" {
		t.Fatalf("无记忆应返回空: %q", got)
	}
}
