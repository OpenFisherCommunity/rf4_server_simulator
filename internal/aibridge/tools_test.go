package aibridge

import (
	"context"
	"database/sql"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRegistryOnlyRunsWhitelistedTools(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "static.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE loc_fish(loc TEXT, fish_key TEXT, name_zh TEXT, catches INTEGER);
		INSERT INTO loc_fish VALUES('lake_a','perch','河鲈',3);
		CREATE TABLE item_catalog(item_id TEXT, name TEXT, name_zh TEXT, kind TEXT, numeric_id INTEGER);
		INSERT INTO item_catalog VALUES('rod_1','Rod','鲤竿','rod',1);`); err != nil {
		t.Fatalf("建测试静态表: %v", err)
	}
	k := &Knowledge{static: db, client: &http.Client{Timeout: time.Second}}
	r := NewRegistry(k)
	ctx := context.Background()

	out := r.Run(ctx, `["species_lookup"]`, "河鲈在哪钓")
	if !strings.Contains(out, "[species_lookup]") || !strings.Contains(out, "河鲈") {
		t.Fatalf("白名单工具未执行或结果缺失: %q", out)
	}
	if strings.Contains(out, "鲤竿") {
		t.Fatalf("执行了白名单外的工具: %q", out)
	}

	itemOut := r.Run(ctx, `["item_lookup"]`, "鲤竿怎么样")
	if !strings.Contains(itemOut, "鲤竿") || strings.Contains(itemOut, "species_lookup") {
		t.Fatalf("工具切换错误: %q", itemOut)
	}
	if got := r.Run(ctx, `[]`, "河鲈"); got != "" {
		t.Fatalf("空白名单不应产出: %q", got)
	}
	if got := r.Run(ctx, `["not_a_tool"]`, "河鲈"); got != "" {
		t.Fatalf("未注册工具不应产出: %q", got)
	}
	if got := r.Run(ctx, `["species_lookup"]`, "hello"); got != "" {
		t.Fatalf("无中文关键词时不应查询: %q", got)
	}
}

func TestRegistrySpecsAndRunOne(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "static2.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE loc_fish(loc TEXT, fish_key TEXT, name_zh TEXT, catches INTEGER);
		INSERT INTO loc_fish VALUES('lake_a','perch','河鲈',3);`); err != nil {
		t.Fatal(err)
	}
	k := &Knowledge{static: db}
	r := NewRegistry(k)
	specs := r.Specs(`["fish_query","species_lookup","not_a_tool"]`)
	if len(specs) != 2 {
		t.Fatalf("工具规格数量 = %d: %+v", len(specs), specs)
	}
	if specs[0].Name != "fish_query" || len(specs[0].Parameters) == 0 {
		t.Fatalf("fish_query 规格错误: %+v", specs[0])
	}
	// fish_query 由 Runner 走跨进程队列，本地 RunOne 返回空。
	if got := r.RunOne(context.Background(), "fish_query", `{"fish":"河鲈"}`); got != "" {
		t.Fatalf("远程工具不应在本地执行: %q", got)
	}
	if got := r.RunOne(context.Background(), "species_lookup", `{"query":"河鲈"}`); !strings.Contains(got, "河鲈") {
		t.Fatalf("RunOne 结果错误: %q", got)
	}
	if got := r.RunOne(context.Background(), "not_a_tool", `{}`); got != "" {
		t.Fatalf("未注册工具不应产出: %q", got)
	}
}
