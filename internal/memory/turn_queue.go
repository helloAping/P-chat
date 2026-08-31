package memory

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrTurnQueueBlocked 表示队首存在 failed 项，必须先处理它。
// ErrTurnQueueBlocked means a failed head item is pausing FIFO draining.
var ErrTurnQueueBlocked = errors.New("turn queue is blocked by a failed item")

// ErrTurnQueueRunning 表示队首已经被其他客户端领取。
// ErrTurnQueueRunning means the FIFO head is already claimed.
var ErrTurnQueueRunning = errors.New("turn queue head is already running")

// ErrTurnQueueNotEditable 表示队列项已经被领取或结束，不能再改写。
// ErrTurnQueueNotEditable means the item is no longer waiting to be claimed.
var ErrTurnQueueNotEditable = errors.New("turn queue item is not editable")

const turnQueueRunningRecoveryAge = 30 * time.Second

const (
	// TurnQueueStatusQueued 表示消息已排队，等待当前回合结束后执行。
	// TurnQueueStatusQueued means the turn is waiting for FIFO execution.
	TurnQueueStatusQueued = "queued"
	// TurnQueueStatusRunning 表示队列项已被前端领取，正在复用 /messages 执行。
	// TurnQueueStatusRunning means the item has been claimed for execution.
	TurnQueueStatusRunning = "running"
	// TurnQueueStatusDone 表示队列项已执行完毕，不再出现在待处理列表里。
	// TurnQueueStatusDone means the item finished and is hidden from pending lists.
	TurnQueueStatusDone = "done"
	// TurnQueueStatusFailed 表示执行前发生传输级失败，需要用户处理后再继续。
	// TurnQueueStatusFailed means a transport failure paused queue draining.
	TurnQueueStatusFailed = "failed"
	// TurnQueueStatusCancelled 表示用户删除了尚未执行的队列项。
	// TurnQueueStatusCancelled means the pending item was removed by the user.
	TurnQueueStatusCancelled = "cancelled"
)

// TurnQueueItem 是一个持久化的会话回合请求。
// TurnQueueItem is one durable queued conversation turn.
type TurnQueueItem struct {
	ID              int64     `json:"id"`
	SessionID       string    `json:"session_id"`
	Status          string    `json:"status"`
	Message         string    `json:"message"`
	PayloadJSON     string    `json:"payload_json,omitempty"`
	ClientMsgID     int64     `json:"client_msg_id"`
	AttachmentCount int       `json:"attachment_count"`
	Error           string    `json:"error,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
	StartedAt       time.Time `json:"started_at,omitempty"`
	FinishedAt      time.Time `json:"finished_at,omitempty"`
}

// CreateTurnQueueItem 追加一个待执行回合。
// CreateTurnQueueItem appends a turn to the durable FIFO queue.
func (s *Store) CreateTurnQueueItem(sessionID string, payloadJSON []byte, message string, clientMsgID int64, attachmentCount int) (TurnQueueItem, error) {
	if sessionID == "" {
		return TurnQueueItem{}, fmt.Errorf("session id is required")
	}
	if message == "" {
		return TurnQueueItem{}, fmt.Errorf("message is required")
	}
	if len(payloadJSON) == 0 {
		return TurnQueueItem{}, fmt.Errorf("payload is required")
	}
	if clientMsgID <= 0 {
		return TurnQueueItem{}, fmt.Errorf("client_msg_id is required")
	}
	if attachmentCount < 0 {
		attachmentCount = 0
	}

	now := time.Now().Unix()
	s.mu.Lock()
	defer s.mu.Unlock()
	res, err := s.db.Exec(
		`INSERT INTO turn_queue(
			session_id, status, message, payload_json, client_msg_id,
			attachment_count, error, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, '', ?, ?)`,
		sessionID, TurnQueueStatusQueued, message, string(payloadJSON),
		clientMsgID, attachmentCount, now, now,
	)
	if err != nil {
		return TurnQueueItem{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return TurnQueueItem{}, err
	}
	return s.getTurnQueueItemByIDLocked(id)
}

// ListTurnQueueItems 返回当前会话尚未结束的队列项。
// ListTurnQueueItems returns pending queue items for one session in FIFO order.
func (s *Store) ListTurnQueueItems(sessionID string) ([]TurnQueueItem, error) {
	if sessionID == "" {
		return nil, nil
	}
	rows, err := s.db.Query(
		`SELECT id, session_id, status, message, payload_json, client_msg_id,
		        attachment_count, error, created_at, updated_at, started_at, finished_at
		   FROM turn_queue
		  WHERE session_id = ?
		    AND status IN (?, ?, ?)
		  ORDER BY id ASC`,
		sessionID, TurnQueueStatusQueued, TurnQueueStatusRunning, TurnQueueStatusFailed,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []TurnQueueItem
	for rows.Next() {
		item, err := scanTurnQueueItem(rows)
		if err != nil {
			return out, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// EditTurnQueueItem 修改尚未领取的队列消息，并同步完整发送载荷。
// EditTurnQueueItem updates an unclaimed message and its persisted send payload.
func (s *Store) EditTurnQueueItem(sessionID string, id int64, message string) (TurnQueueItem, bool, error) {
	message = strings.TrimSpace(message)
	if sessionID == "" || id <= 0 || message == "" {
		return TurnQueueItem{}, false, nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	item, err := s.getTurnQueueItemByIDLocked(id)
	if err == sql.ErrNoRows {
		return TurnQueueItem{}, false, nil
	}
	if err != nil {
		return TurnQueueItem{}, false, fmt.Errorf("get turn queue item %d: %w", id, err)
	}
	if item.SessionID != sessionID {
		return TurnQueueItem{}, false, nil
	}
	if item.Status != TurnQueueStatusQueued {
		return TurnQueueItem{}, false, ErrTurnQueueNotEditable
	}

	payloadJSON, err := rewriteTurnQueuePayloadMessage(item.PayloadJSON, message)
	if err != nil {
		return TurnQueueItem{}, false, fmt.Errorf("rewrite queue payload message: %w", err)
	}
	now := time.Now().Unix()
	res, err := s.db.Exec(
		`UPDATE turn_queue
		    SET message = ?, payload_json = ?, updated_at = ?
		  WHERE id = ? AND session_id = ? AND status = ?`,
		message, payloadJSON, now, id, sessionID, TurnQueueStatusQueued,
	)
	if err != nil {
		return TurnQueueItem{}, false, fmt.Errorf("update turn queue item %d: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return TurnQueueItem{}, false, fmt.Errorf("read edited turn queue rows %d: %w", id, err)
	}
	if n == 0 {
		return TurnQueueItem{}, false, nil
	}
	edited, err := s.getTurnQueueItemByIDLocked(id)
	if err != nil {
		return TurnQueueItem{}, false, fmt.Errorf("reload edited turn queue item %d: %w", id, err)
	}
	return edited, true, nil
}

// ClaimNextTurnQueueItem 领取当前会话的下一条 queued 回合。
// ClaimNextTurnQueueItem marks the next queued turn as running.
func (s *Store) ClaimNextTurnQueueItem(sessionID string) (TurnQueueItem, bool, error) {
	if sessionID == "" {
		return TurnQueueItem{}, false, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.Begin()
	if err != nil {
		return TurnQueueItem{}, false, err
	}
	defer tx.Rollback()

	item, err := scanTurnQueueItem(tx.QueryRow(
		`SELECT id, session_id, status, message, payload_json, client_msg_id,
		        attachment_count, error, created_at, updated_at, started_at, finished_at
		   FROM turn_queue
		  WHERE session_id = ? AND status IN (?, ?, ?)
		  ORDER BY id ASC
		  LIMIT 1`,
		sessionID, TurnQueueStatusQueued, TurnQueueStatusFailed, TurnQueueStatusRunning,
	))
	if err == sql.ErrNoRows {
		return TurnQueueItem{}, false, nil
	}
	if err != nil {
		return TurnQueueItem{}, false, err
	}
	if item.Status == TurnQueueStatusFailed {
		return TurnQueueItem{}, false, ErrTurnQueueBlocked
	}
	if item.Status == TurnQueueStatusRunning {
		return TurnQueueItem{}, false, ErrTurnQueueRunning
	}

	now := time.Now().Unix()
	if _, err := tx.Exec(
		`UPDATE turn_queue
		    SET status = ?, started_at = COALESCE(started_at, ?), updated_at = ?, error = ''
		  WHERE id = ? AND status = ?`,
		TurnQueueStatusRunning, now, now, item.ID, TurnQueueStatusQueued,
	); err != nil {
		return TurnQueueItem{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return TurnQueueItem{}, false, err
	}
	claimed, err := s.getTurnQueueItemByIDLocked(item.ID)
	return claimed, true, err
}

// CompleteTurnQueueItem 标记队列项已完成。
// CompleteTurnQueueItem marks a running item as done.
func (s *Store) CompleteTurnQueueItem(sessionID string, id int64) (TurnQueueItem, bool, error) {
	return s.finishTurnQueueItem(sessionID, id, TurnQueueStatusDone, "", TurnQueueStatusRunning)
}

// FailTurnQueueItem 标记队列项失败并暂停自动出队。
// FailTurnQueueItem marks an item failed so the client can pause draining.
func (s *Store) FailTurnQueueItem(sessionID string, id int64, message string) (TurnQueueItem, bool, error) {
	return s.finishTurnQueueItem(sessionID, id, TurnQueueStatusFailed, message, TurnQueueStatusRunning)
}

func (s *Store) finishTurnQueueItem(sessionID string, id int64, status, message, fromStatus string) (TurnQueueItem, bool, error) {
	if sessionID == "" || id <= 0 {
		return TurnQueueItem{}, false, nil
	}
	now := time.Now().Unix()
	s.mu.Lock()
	defer s.mu.Unlock()
	res, err := s.db.Exec(
		`UPDATE turn_queue
		    SET status = ?, error = ?, finished_at = ?, updated_at = ?
		  WHERE id = ? AND session_id = ? AND status = ?`,
		status, message, now, now, id, sessionID, fromStatus,
	)
	if err != nil {
		return TurnQueueItem{}, false, err
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return TurnQueueItem{}, false, nil
	}
	item, err := s.getTurnQueueItemByIDLocked(id)
	if err == sql.ErrNoRows {
		return TurnQueueItem{}, false, nil
	}
	return item, true, err
}

// RequeueFailedTurnQueueItem 将失败项放回队列，供用户重试。
// RequeueFailedTurnQueueItem moves a failed item back to queued.
func (s *Store) RequeueFailedTurnQueueItem(sessionID string, id int64) (TurnQueueItem, bool, error) {
	if sessionID == "" || id <= 0 {
		return TurnQueueItem{}, false, nil
	}
	now := time.Now().Unix()
	s.mu.Lock()
	defer s.mu.Unlock()
	res, err := s.db.Exec(
		`UPDATE turn_queue
		    SET status = ?, error = '', started_at = NULL, finished_at = NULL, updated_at = ?
		  WHERE id = ? AND session_id = ? AND status = ?`,
		TurnQueueStatusQueued, now, id, sessionID, TurnQueueStatusFailed,
	)
	if err != nil {
		return TurnQueueItem{}, false, err
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return TurnQueueItem{}, false, nil
	}
	item, err := s.getTurnQueueItemByIDLocked(id)
	if err == sql.ErrNoRows {
		return TurnQueueItem{}, false, nil
	}
	return item, true, err
}

func rewriteTurnQueuePayloadMessage(payloadJSON, message string) (string, error) {
	var payload map[string]any
	if err := json.Unmarshal([]byte(payloadJSON), &payload); err != nil {
		return "", err
	}
	payload["message"] = message
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// RequeueRunningTurnQueueItems 恢复被前端刷新中断的 running 项。
// RequeueRunningTurnQueueItems reopens claimed items after a disconnected UI.
func (s *Store) RequeueRunningTurnQueueItems(sessionID string) error {
	if sessionID == "" {
		return nil
	}
	now := time.Now().Unix()
	cutoff := now - int64(turnQueueRunningRecoveryAge/time.Second)
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(
		`UPDATE turn_queue
		    SET status = ?, error = '', started_at = NULL, updated_at = ?
		  WHERE session_id = ? AND status = ? AND (started_at IS NULL OR started_at <= ?)`,
		TurnQueueStatusQueued, now, sessionID, TurnQueueStatusRunning, cutoff,
	)
	return err
}

// CancelTurnQueueItem 删除一条尚未执行或失败的队列项。
// CancelTurnQueueItem marks a queued or failed item as cancelled.
func (s *Store) CancelTurnQueueItem(sessionID string, id int64) (TurnQueueItem, bool, error) {
	if sessionID == "" || id <= 0 {
		return TurnQueueItem{}, false, nil
	}
	now := time.Now().Unix()
	s.mu.Lock()
	defer s.mu.Unlock()
	res, err := s.db.Exec(
		`UPDATE turn_queue
		    SET status = ?, finished_at = ?, updated_at = ?
		  WHERE id = ? AND session_id = ? AND status IN (?, ?)`,
		TurnQueueStatusCancelled, now, now, id, sessionID,
		TurnQueueStatusQueued, TurnQueueStatusFailed,
	)
	if err != nil {
		return TurnQueueItem{}, false, err
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return TurnQueueItem{}, false, nil
	}
	item, err := s.getTurnQueueItemByIDLocked(id)
	if err == sql.ErrNoRows {
		return TurnQueueItem{}, false, nil
	}
	return item, true, err
}

// ClearTurnQueue 取消当前会话尚未执行或失败的队列项。
// ClearTurnQueue cancels all queued or failed items for one session.
func (s *Store) ClearTurnQueue(sessionID string) (int64, error) {
	if sessionID == "" {
		return 0, nil
	}
	now := time.Now().Unix()
	s.mu.Lock()
	defer s.mu.Unlock()
	res, err := s.db.Exec(
		`UPDATE turn_queue
		    SET status = ?, finished_at = ?, updated_at = ?
		  WHERE session_id = ? AND status IN (?, ?)`,
		TurnQueueStatusCancelled, now, now, sessionID,
		TurnQueueStatusQueued, TurnQueueStatusFailed,
	)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (s *Store) getTurnQueueItemByIDLocked(id int64) (TurnQueueItem, error) {
	return scanTurnQueueItem(s.db.QueryRow(
		`SELECT id, session_id, status, message, payload_json, client_msg_id,
		        attachment_count, error, created_at, updated_at, started_at, finished_at
		   FROM turn_queue WHERE id = ?`,
		id,
	))
}

type turnQueueScanner interface {
	Scan(dest ...any) error
}

func scanTurnQueueItem(scanner turnQueueScanner) (TurnQueueItem, error) {
	var (
		item                  TurnQueueItem
		createdAt, updatedAt  int64
		startedAt, finishedAt sql.NullInt64
	)
	if err := scanner.Scan(
		&item.ID, &item.SessionID, &item.Status, &item.Message, &item.PayloadJSON,
		&item.ClientMsgID, &item.AttachmentCount, &item.Error,
		&createdAt, &updatedAt, &startedAt, &finishedAt,
	); err != nil {
		return TurnQueueItem{}, err
	}
	item.CreatedAt = time.Unix(createdAt, 0)
	item.UpdatedAt = time.Unix(updatedAt, 0)
	item.StartedAt = timeFromNullableUnix(startedAt)
	item.FinishedAt = timeFromNullableUnix(finishedAt)
	return item, nil
}
