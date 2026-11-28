package gameserver

import (
	"context"
	"database/sql"

	"rf4server/internal/proto"
	"rf4server/internal/store"
)

// ensureAwardsInHouse 把玩家背包/鱼护里的奖赏物品移进房屋容器（容器 10）。
//
// 房屋里「奖赏」展示位按 E 后，客户端用 4/1(10)/4/4(10)/4/5(10) 拉房屋
// 容器；只有 `item.container='house'` 的物品才会出现在这个列表里。奖赏
// 物品若留在背包，3/16 奖赏面板仍能显示（它读 house/backpack/keepnet），
// 但展示位列表永远是空的，客户端显示「未找到一件合适的物件」。
//
// 因此首次请求背包或房屋库存时做一次幂等搬迁：只搬 enikeammfen(2015)
// 奖赏，受房屋容量限制，失败不改动。奖赏本身是纪念品，放进房屋后仍由
// 3/16 面板展示，也才能被摆到展示位上。
func (s *Server) ensureAwardsInHouse(ctx context.Context, uid int64) (int, error) {
	if s == nil || s.DB == nil {
		return 0, nil
	}
	// 快路径：没有待搬奖赏就不开事务。这里的读取只用于跳过工作，
	// 真正的容量与待搬集合仍在事务内重新读取。
	rows, err := s.playerRewardItems(ctx, uid)
	if err != nil {
		return 0, err
	}
	hasPending := false
	for _, it := range rows {
		if it.Container != "house" {
			hasPending = true
			break
		}
	}
	if !hasPending {
		return 0, nil
	}
	// 导入容量是静态 KV，可在事务外读；占用与待搬奖赏必须在同一事务里
	// 重新读取，否则并发搬迁会各自基于旧占用放行，突破房屋上限。
	capacity, _ := s.importedContainerCapacity(ctx, uid, 10, houseContainerCapacity, 0)
	tx, err := beginEconomyTx(ctx, s.DB)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	var used int64
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM item WHERE uid=? AND container='house'`, uid).Scan(&used); err != nil {
		return 0, err
	}
	rows, err = playerRewardItemsTx(ctx, tx, uid)
	if err != nil {
		return 0, err
	}
	pending := make([]*store.Item, 0, len(rows))
	for _, it := range rows {
		if it.Container != "house" {
			pending = append(pending, it)
		}
	}
	room := capacity - used
	if room <= 0 || len(pending) == 0 {
		return 0, nil
	}
	if int64(len(pending)) > room {
		pending = pending[:room]
	}
	for _, it := range pending {
		if _, err := tx.ExecContext(ctx,
			`UPDATE item SET container='house' WHERE guid=? AND uid=? AND container<>'house'`,
			it.GUID, uid); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return len(pending), nil
}

// playerRewardItemsTx 在事务内读取玩家奖赏行。store.DB 单连接下事务持有期间
// 不能再调用 DB 方法，所以这里直接用 *sql.Tx 查询。
func playerRewardItemsTx(ctx context.Context, tx *sql.Tx, uid int64) ([]*store.Item, error) {
	rows, err := tx.QueryContext(ctx, `SELECT `+txItemCols+`
		FROM item WHERE uid=? AND container IN ('house','backpack','keepnet')
		ORDER BY created_ts, guid`, uid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]*store.Item, 0)
	for rows.Next() {
		it, err := scanTxItem(rows)
		if err != nil {
			return nil, err
		}
		if it.WireClass == "enikeammfen" && it.TypeID == 2015 {
			out = append(out, it)
		}
	}
	return out, rows.Err()
}

// syncAwardGroupOnce 把玩家全部奖赏按 f4=14（客户端奖赏专用分组）推给
// 本会话一次。客户端「选择物件」的奖赏标签读的是物品管理器分组 14，
// 不是 3/16 面板数组，也不会自动从 4/4 房屋列表推断；不推就会出现
// 「3/16 面板有、摆放列表空」。每会话只推一次，避免每次开背包刷屏。
func (s *Session) syncAwardGroupOnce() {
	if s == nil || s.Srv == nil || s.Srv.DB == nil {
		return
	}
	s.awardsSyncMu.Lock()
	defer s.awardsSyncMu.Unlock()
	if s.awardsSynced {
		return
	}
	ctx := s.pushCtx()
	rows, err := s.Srv.playerRewardItems(ctx, s.Userid)
	if err != nil {
		return
	}
	trees := make([]*proto.Value, 0, len(rows))
	for _, it := range rows {
		tree, terr := s.Srv.awardItemTree(ctx, it)
		if terr != nil || tree == nil {
			return
		}
		trees = append(trees, tree)
	}
	if len(trees) > 0 {
		if s.Push(4, 22, proto.Args(proto.S("61"), trees...)) == 0 {
			return
		}
	}
	s.awardsSynced = true
}

// pushAwardItems 把新发奖赏按奖赏分组（f4=14）推给本会话。新增奖赏的
// 增量路径不能复用普通物品树：普通树按 house=10 投影，客户端奖赏标签
// 读分组 14，会把刚拿到的奖座过滤掉。
func (s *Session) pushAwardItems(items []*store.Item) {
	if s == nil || s.Srv == nil || len(items) == 0 {
		return
	}
	ctx := s.pushCtx()
	trees := make([]*proto.Value, 0, len(items))
	for _, it := range items {
		if it == nil {
			continue
		}
		tree, err := s.Srv.awardItemTree(ctx, it)
		if err == nil && tree != nil {
			trees = append(trees, tree)
		}
	}
	if len(trees) > 0 {
		s.Push(4, 22, proto.Args(proto.S("61"), trees...))
	}
}
