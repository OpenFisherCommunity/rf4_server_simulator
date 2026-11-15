package gameserver

// L1 协议接入：AI 私聊请求的入队与 AI 回复的投递。
// 关键边界：只有 source_kind=human 的消息能进 ai_task；AI 回复只写 mail/推送，
// 绝不回流，从结构上杜绝 Agent ↔ Agent 循环。

import (
	"context"
	"fmt"
	"strings"
	"time"

	"rf4server/internal/store"
)

const (
	aiTaskTTL          = 30 * time.Second
	aiMaxMessageRunes  = 500
	aiMaxDeliveryBatch = 16
)

// receiveAIChat 处理 19/1 发给某个 AI 联系人的私聊。立即 ACK，不等待模型。
func (s *Server) receiveAIChat(ctx *Ctx, contactUID int64, agentKey, text string) ([]byte, bool) {
	if !ctx.ExpectResponse {
		return nil, true
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return []byte{0}, true
	}
	if r := []rune(text); len(r) > aiMaxMessageRunes {
		text = string(r[:aiMaxMessageRunes])
	}
	deadline := float64(time.Now().Add(aiTaskTTL).Unix())
	// 连发消息合并成一次请求：既省 token，也避免 AI 对半句话逐条回复。
	merged, mergeErr := s.DB.AIMergePendingDM(ctx.Sess.pushCtx(), ctx.Sess.Userid,
		agentKey, text, deadline, aiMaxMessageRunes)
	if mergeErr != nil {
		s.log.Warn("AI 消息合并失败", "uid", ctx.Sess.Userid, "agent", agentKey, "err", mergeErr)
	}
	if merged {
		return []byte{0}, true
	}
	_, err := s.DB.AIEnqueue(ctx.Sess.pushCtx(), &store.AITask{
		Kind:       "dm",
		Scope:      "uid",
		ScopeKey:   fmt.Sprint(ctx.Sess.Userid),
		UID:        ctx.Sess.Userid,
		Agent:      agentKey,
		ContactUID: contactUID,
		SourceKind: "human",
		Text:       text,
		Loc:        s.currentLevel(ctx.Sess.Userid),
		DeadlineTS: deadline,
	})
	if err != nil {
		s.log.Warn("AI 任务入队失败", "uid", ctx.Sess.Userid, "agent", agentKey, "err", err)
	}
	return []byte{0}, true
}

// pollAIResults 由 StartAdminSubscriber 的 2 秒 ticker 调用。
func (s *Server) pollAIResults(ctx context.Context, db *store.DB) {
	if db == nil {
		return
	}
	results, err := db.AIPendingResults(ctx, aiMaxDeliveryBatch)
	if err != nil {
		s.log.Warn("AI 结果读取失败", "err", err)
		return
	}
	// 频道消息每批只推一条：圆桌 6 条会分散到多个 2 秒 tick，避免客户端瞬间刷屏。
	channelSent := false
	for _, r := range results {
		if r.Kind == "channel" {
			if channelSent {
				continue
			}
			channelSent = true
		}
		ok := s.deliverAIResult(ctx, db, r)
		if err := db.AIMarkDelivered(ctx, r.ID, ok); err != nil {
			s.log.Warn("AI 结果标记失败", "id", r.ID, "err", err)
		}
	}
}

// deliverAIResult 把回复写进 mail 表，并在 ACK 之后通知在线会话。
func (s *Server) deliverAIResult(ctx context.Context, db *store.DB, r store.AIResult) bool {
	if r.ReplyText == "" || r.ContactUID == 0 {
		return false
	}
	if r.Kind == "channel" {
		return s.deliverAIChannel(r)
	}
	if r.UID == 0 {
		return false
	}
	tx, err := db.SQL.BeginTx(ctx, nil)
	if err != nil {
		s.log.Warn("AI 回复事务开启失败", "err", err)
		return false
	}
	defer func() { _ = tx.Rollback() }()
	mailID, err := insertContactReply(ctx, tx, r.UID, r.ContactUID, r.ReplyText)
	if err != nil {
		s.log.Warn("AI 回复写入失败", "err", err)
		return false
	}
	// 邮件与「已投递」同一事务提交：进程在两步之间崩溃也不会重复投递。
	res, err := tx.ExecContext(ctx,
		`UPDATE ai_result SET delivered_ts=?, reply_text='' WHERE id=? AND delivered_ts IS NULL`,
		float64(time.Now().Unix()), r.ID)
	if err != nil {
		s.log.Warn("AI 回复标记失败", "id", r.ID, "err", err)
		return false
	}
	if n, _ := res.RowsAffected(); n == 0 {
		// 另一轮投递已经处理过这条结果，回滚本次邮件插入。
		return false
	}
	if err := tx.Commit(); err != nil {
		s.log.Warn("AI 回复提交失败", "err", err)
		return false
	}

	s.mu.Lock()
	var target *Session
	for _, sess := range s.sessions {
		if sess != nil && !sess.Closed() && sess.Userid == r.UID {
			target = sess
			break
		}
	}
	s.mu.Unlock()
	if target != nil {
		s.notifyMail(target.pushCtx(), r.UID, mailID)
	}
	return true
}
