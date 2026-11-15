package aibridge

// 内部只读工具注册表。Agent 只能用自己 tools_json 白名单里的工具；
// 所有查询都走只读静态库（query_only=ON），不写玩家库、不碰静态库。

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

type internalTool func(ctx context.Context, k *Knowledge, keywords []string) string

// Registry 按名字注册只读工具。
type Registry struct {
	knowledge *Knowledge
	tools     map[string]internalTool
}

// NewRegistry 注册内部只读资料工具。
func NewRegistry(k *Knowledge) *Registry {
	return &Registry{knowledge: k, tools: map[string]internalTool{
		"species_lookup": toolSpecies,
		"bait_pref":      toolBaitPref,
		"spot_lookup":    toolSpot,
		"item_lookup":    toolItem,
		"shop_price":     toolShopPrice,
		"recipe_lookup":  toolRecipe,
	}}
}

// keywordsOf 把连续中文切成 2 字滑窗：整句 "河鲈在哪钓" 直接 LIKE 命中不了
// "河鲈"，必须切成 "河鲈"/"鲈在"/"在哪" 这类片段。
func keywordsOf(query string) []string {
	matches := hanWords.FindAllString(query, 3)
	out := make([]string, 0, 3)
	seen := map[string]bool{}
	add := func(s string) {
		if len([]rune(s)) < 2 || seen[s] {
			return
		}
		seen[s] = true
		out = append(out, "%"+s+"%")
	}
	for _, m := range matches {
		r := []rune(m)
		if len(r) <= 4 {
			add(m)
		}
		for i := 0; i+2 <= len(r) && len(out) < 3; i++ {
			add(string(r[i : i+2]))
		}
		if len(out) >= 3 {
			break
		}
	}
	return out
}

// Run 按 Agent 白名单执行全部启用的工具，结果汇总为可注入提示词的文本。
func (r *Registry) Run(ctx context.Context, toolsJSON, query string) string {
	if r == nil || r.knowledge == nil || r.knowledge.static == nil {
		return ""
	}
	var names []string
	if err := json.Unmarshal([]byte(toolsJSON), &names); err != nil {
		return ""
	}
	keywords := keywordsOf(query)
	if len(keywords) == 0 {
		return ""
	}
	var b strings.Builder
	for _, name := range names {
		fn, ok := r.tools[name]
		if !ok {
			continue
		}
		if out := fn(ctx, r.knowledge, keywords); out != "" {
			fmt.Fprintf(&b, "[%s]\n%s\n", name, out)
		}
		if b.Len() > 1600 {
			break
		}
	}
	return trimRunes(b.String(), 1600)
}

func scanPairs(ctx context.Context, k *Knowledge, query string, args []any, format func(a, b string) string, limit int) string {
	rows, err := k.static.QueryContext(ctx, query, args...)
	if err != nil {
		return ""
	}
	defer rows.Close()
	var b strings.Builder
	n := 0
	for rows.Next() && n < limit {
		var a, c string
		if rows.Scan(&a, &c) == nil {
			b.WriteString(format(a, c))
			n++
		}
	}
	return b.String()
}

func toolSpecies(ctx context.Context, k *Knowledge, kws []string) string {
	var out strings.Builder
	for _, kw := range kws {
		out.WriteString(scanPairs(ctx, k,
			`SELECT name_zh, loc FROM loc_fish WHERE name_zh LIKE ? LIMIT 4`,
			[]any{kw}, func(a, b string) string { return fmt.Sprintf("鱼种:%s 水域:%s\n", a, b) }, 4))
	}
	return out.String()
}

func toolBaitPref(ctx context.Context, k *Knowledge, kws []string) string {
	var out strings.Builder
	for _, kw := range kws {
		out.WriteString(scanPairs(ctx, k,
			`SELECT DISTINCT COALESCE(lf.name_zh, p.fish_key), p.bait_zh
			 FROM fish_bait_pref p
			 LEFT JOIN loc_fish lf ON lf.fish_key=p.fish_key
			 WHERE p.bait_zh LIKE ? LIMIT 4`,
			[]any{kw}, func(a, b string) string { return fmt.Sprintf("鱼:%s 饵:%s\n", a, b) }, 4))
	}
	return out.String()
}

func toolSpot(ctx context.Context, k *Knowledge, kws []string) string {
	var out strings.Builder
	for _, kw := range kws {
		out.WriteString(scanPairs(ctx, k,
			`SELECT s.loc, s.spot FROM spot_fish s
			 LEFT JOIN loc_fish lf ON lf.fish_key=s.fish_key AND lf.loc=s.loc
			 WHERE s.fish_key LIKE ? OR s.loc LIKE ? OR lf.name_zh LIKE ? LIMIT 4`,
			[]any{kw, kw, kw}, func(a, b string) string { return fmt.Sprintf("水域:%s 点位:%s\n", a, b) }, 4))
	}
	return out.String()
}

func toolItem(ctx context.Context, k *Knowledge, kws []string) string {
	var out strings.Builder
	for _, kw := range kws {
		out.WriteString(scanPairs(ctx, k,
			`SELECT name_zh, item_id FROM item_catalog WHERE name_zh LIKE ? LIMIT 4`,
			[]any{kw}, func(a, b string) string { return fmt.Sprintf("物品:%s id:%s\n", a, b) }, 4))
	}
	return out.String()
}

func toolShopPrice(ctx context.Context, k *Knowledge, kws []string) string {
	var out strings.Builder
	for _, kw := range kws {
		out.WriteString(scanPairs(ctx, k,
			`SELECT COALESCE(c.name_zh, p.item_id), CAST(p.silver AS TEXT)
			 FROM item_price p LEFT JOIN item_catalog c ON c.item_id=p.item_id
			 WHERE p.item_id LIKE ? OR c.name_zh LIKE ? LIMIT 4`,
			[]any{kw, kw}, func(a, b string) string { return fmt.Sprintf("商品:%s 银币:%s\n", a, b) }, 4))
	}
	return out.String()
}

func toolRecipe(ctx context.Context, k *Knowledge, kws []string) string {
	var out strings.Builder
	for _, kw := range kws {
		out.WriteString(scanPairs(ctx, k,
			`SELECT r.name, COALESCE(c.name_zh, r.output_id)
			 FROM recipe r LEFT JOIN item_catalog c ON c.item_id=r.output_id
			 WHERE r.name LIKE ? LIMIT 4`,
			[]any{kw}, func(a, b string) string { return fmt.Sprintf("配方:%s 产物:%s\n", a, b) }, 4))
	}
	return out.String()
}

// firstFishKeyword 从自然语言里取第一个中文 2 字片段作为鱼名，
// 交给 gameserver 的 fishQueryReply 解析（它期望「鱼名 [地图]」）。
func firstFishKeyword(query string) string {
	kws := keywordsOf(query)
	if len(kws) == 0 {
		return ""
	}
	return strings.Trim(kws[0], "%")
}

// toolDefinitions 是交给模型选择的函数清单（function calling）。
var toolDefinitions = map[string]ToolSpec{
	"fish_query": {
		Name:        "fish_query",
		Description: "按鱼名查询推荐水域、饵、钓法与理论条每小时。结果来自服务端产线权重链，只读。",
		Parameters:  json.RawMessage(`{"type":"object","properties":{"fish":{"type":"string","description":"鱼名，例如 河鲈"}},"required":["fish"]}`),
	},
	"species_lookup": {
		Name:        "species_lookup",
		Description: "查鱼种出现在哪些水域。",
		Parameters:  json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}`),
	},
	"bait_pref": {
		Name:        "bait_pref",
		Description: "查鱼种与饵料的对应关系。",
		Parameters:  json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}`),
	},
	"spot_lookup": {
		Name:        "spot_lookup",
		Description: "查水域点位与鱼种。",
		Parameters:  json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}`),
	},
	"item_lookup": {
		Name:        "item_lookup",
		Description: "按中文名查装备与物品。",
		Parameters:  json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}`),
	},
	"shop_price": {
		Name:        "shop_price",
		Description: "查商品价格与库存。",
		Parameters:  json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}`),
	},
	"recipe_lookup": {
		Name:        "recipe_lookup",
		Description: "查制作配方与产物。",
		Parameters:  json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}`),
	},
	// 陪玩专用：只看天气和本图有哪些鱼，故意不含饵料，
	// 免得每个角色都退化成查饵机器人。
	"map_vibe": {
		Name:        "map_vibe",
		Description: "看某张图此刻的天气和这片水域有哪些鱼。只有鱼种，不含饵料和点位，适合闲聊时顺口一提。",
		Parameters:  json.RawMessage(`{"type":"object","properties":{"loc":{"type":"string","description":"地图中文名或 level key，留空表示当前所在地图"}}}`),
	},
}

// Specs 按 Agent 白名单生成模型可见的工具清单。
func (r *Registry) Specs(toolsJSON string) []ToolSpec {
	var names []string
	if err := json.Unmarshal([]byte(toolsJSON), &names); err != nil {
		return nil
	}
	out := make([]ToolSpec, 0, len(names))
	for _, name := range names {
		if spec, ok := toolDefinitions[name]; ok {
			out = append(out, spec)
		}
	}
	return out
}

// RunOne 执行单个本地工具；fish_query 由 Runner 走跨进程队列，不在这里执行。
func (r *Registry) RunOne(ctx context.Context, name, argsJSON string) string {
	fn, ok := r.tools[name]
	if !ok || r.knowledge == nil || r.knowledge.static == nil {
		return ""
	}
	var args struct {
		Query string `json:"query"`
		Fish  string `json:"fish"`
	}
	_ = json.Unmarshal([]byte(argsJSON), &args)
	q := args.Query
	if q == "" {
		q = args.Fish
	}
	if q == "" {
		return ""
	}
	return trimRunes(fn(ctx, r.knowledge, keywordsOf(q)), 800)
}
