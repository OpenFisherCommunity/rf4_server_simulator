package aibridge

import "testing"

func TestToolArg(t *testing.T) {
	if got := toolArg(`{"fish":"河鲈"}`, "fish"); got != "河鲈" {
		t.Fatalf("fish = %q", got)
	}
	if got := toolArg(`not json`, "fish"); got != "" {
		t.Fatalf("坏 JSON 应返回空: %q", got)
	}
	if got := toolArg(`{"query":"鲤竿"}`, "fish"); got != "" {
		t.Fatalf("缺字段应返回空: %q", got)
	}
}
