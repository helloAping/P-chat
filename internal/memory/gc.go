package memory

import (
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/p-chat/pchat/internal/llm"
)

const (
	// StorageGCKeepRecent is how many trailing messages in each
	// conversation keep their tool output and thinking text.
	StorageGCKeepRecent = 40
	// StorageGCBlankGrace keeps a message-less conversation that was
	// touched recently, so the blank slot a new chat just reused is
	// not deleted out from under the user.
	StorageGCBlankGrace = 24 * time.Hour

	storageGCToolKeep  = 1500
	storageGCToolMark  = "\n…[历史工具输出已回收]"
	storageGCImageMark = "[图片已回收]"
)

// StorageGCOptions selects which storage garbage-collection passes to run.
// Apply false previews savings without writing.
type StorageGCOptions struct {
	Apply              bool
	InlineImages       bool
	HistoricalPayload  bool
	BlankConversations bool
	FinishedQueue      bool
	StaleSummaries     bool
	Vacuum             bool
	// KeepRecent overrides StorageGCKeepRecent when positive.
	KeepRecent int
}

// StorageGCItem is one reclaimable category.
type StorageGCItem struct {
	ID     string `json:"id"`
	Label  string `json:"label"`
	Detail string `json:"detail"`
	Count  int    `json:"count"`
	Bytes  int64  `json:"bytes"`
}

// StorageGCReport is the preview or the result of a storage GC pass.
type StorageGCReport struct {
	Items       []StorageGCItem `json:"items"`
	TotalBytes  int64           `json:"total_bytes"`
	Vacuumed    bool            `json:"vacuumed"`
	VacuumError string          `json:"vacuum_error,omitempty"`
}

// ImageStore writes image bytes and returns the upload id that
// messages will reference as upl://<id>. Preview passes nil.
type ImageStore func(convID, mime string, data []byte) (string, error)

type imageExternalizer struct {
	apply  bool
	convID string
	store  ImageStore
	cache  map[string]string
}

func (e *imageExternalizer) fileID(mime string, data []byte) (string, error) {
	sum := sha256.Sum256(data)
	key := mime + ":" + string(sum[:])
	if id, ok := e.cache[key]; ok {
		return id, nil
	}
	if !e.apply {
		id := "0000000000000000"
		e.cache[key] = id
		return id, nil
	}
	if e.store == nil {
		return "", fmt.Errorf("image store is not configured")
	}
	id, err := e.store(e.convID, mime, data)
	if err != nil {
		return "", err
	}
	e.cache[key] = id
	return id, nil
}

// rewriteImageString replaces a whole-value image payload.
// asFile keeps a viewable upl:// reference; otherwise the pixels are dropped.
func (e *imageExternalizer) rewriteImageString(s string, asFile bool) (string, bool) {
	mime, data, ok := decodeImagePayload(s)
	if !ok {
		return s, false
	}
	if !asFile {
		return storageGCImageMark, true
	}
	id, err := e.fileID(mime, data)
	if err != nil {
		return s, false
	}
	return uplRefPrefix + id, true
}

var dataImageRE = regexp.MustCompile(`data:image/[a-zA-Z0-9.+-]+;base64,[A-Za-z0-9+/=\r\n]{80,}`)

func (e *imageExternalizer) rewriteEmbedded(s string) string {
	if s == "" || !strings.Contains(s, "data:image/") {
		return s
	}
	return dataImageRE.ReplaceAllStringFunc(s, func(match string) string {
		next, ok := e.rewriteImageString(match, false)
		if !ok {
			return match
		}
		return next
	})
}

func decodeImagePayload(s string) (mime string, data []byte, ok bool) {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "data:image/") {
		rest := strings.TrimPrefix(s, "data:")
		semi := strings.IndexByte(rest, ';')
		comma := strings.IndexByte(rest, ',')
		if semi <= 0 || comma <= semi {
			return "", nil, false
		}
		mime = rest[:semi]
		payload := strings.TrimSpace(rest[comma+1:])
		if len(payload) < 80 {
			return "", nil, false
		}
		decoded, err := base64.StdEncoding.DecodeString(payload)
		if err != nil || len(decoded) < 32 {
			return "", nil, false
		}
		return mime, decoded, true
	}
	if len(s) < 80 || strings.ContainsAny(s, " \t\r\n") {
		return "", nil, false
	}
	switch {
	case strings.HasPrefix(s, "iVBORw0KGgo"):
		mime = "image/png"
	case strings.HasPrefix(s, "/9j/"):
		mime = "image/jpeg"
	case strings.HasPrefix(s, "R0lGOD"):
		mime = "image/gif"
	case strings.HasPrefix(s, "UklGR"):
		mime = "image/webp"
	default:
		return "", nil, false
	}
	decoded, err := base64.StdEncoding.DecodeString(s)
	if err != nil || len(decoded) < 32 {
		return "", nil, false
	}
	return mime, decoded, true
}

// StorageGC previews or applies storage reclaim. Image files are written
// only when Apply is set and store is non-nil.
func (s *Store) StorageGC(opts StorageGCOptions, store ImageStore) (StorageGCReport, error) {
	if err := s.Flush(); err != nil {
		return StorageGCReport{}, err
	}
	keep := opts.KeepRecent
	if keep <= 0 {
		keep = StorageGCKeepRecent
	}
	ext := &imageExternalizer{apply: opts.Apply, store: store, cache: map[string]string{}}

	report := StorageGCReport{}
	var imageItem, histItem StorageGCItem
	imageItem.ID = "inline_images"
	imageItem.Label = "对话中的 base64 图片"
	imageItem.Detail = "用户图片改存为上传文件并继续显示。工具卡片和思考里的内嵌图片只留占位，不再保存像素。"
	histItem.ID = "historical_payload"
	histItem.Label = "历史回合的工具输出和思考"
	histItem.Detail = fmt.Sprintf("每个对话保留最近 %d 条消息的工具输出和思考全文。更早的工具输出截断，思考过程删除。用户原文和助手最终回复保留。", keep)

	if opts.InlineImages || opts.HistoricalPayload {
		protected, err := s.recentMessageIDs(keep)
		if err != nil {
			return StorageGCReport{}, err
		}
		rows, err := s.db.Query(`SELECT id, conversation_id, role, content, COALESCE(metadata,''), msg_type FROM messages`)
		if err != nil {
			return StorageGCReport{}, err
		}
		type payloadUpdate struct {
			id      int64
			content string
			meta    string
			msgType int
		}
		var updates []payloadUpdate
		for rows.Next() {
			var id int64
			var conv, role, content, meta string
			var msgType int
			if err := rows.Scan(&id, &conv, &role, &content, &meta, &msgType); err != nil {
				rows.Close()
				return StorageGCReport{}, err
			}
			ext.convID = conv
			_, protectedRow := protected[id]
			nextContent, nextMeta, nextType, imageSaved, histSaved, changed := rewriteMessagePayload(
				role, content, meta, msgType, protectedRow, opts, ext,
			)
			if imageSaved > 0 {
				imageItem.Count++
				imageItem.Bytes += int64(imageSaved)
			}
			if histSaved > 0 {
				histItem.Count++
				histItem.Bytes += int64(histSaved)
			}
			if changed && opts.Apply {
				updates = append(updates, payloadUpdate{id, nextContent, nextMeta, nextType})
			}
		}
		scanErr := rows.Err()
		rows.Close()
		if scanErr != nil {
			return StorageGCReport{}, scanErr
		}
		for _, u := range updates {
			if _, err := s.db.Exec(
				`UPDATE messages SET content = ?, metadata = ?, msg_type = ? WHERE id = ?`,
				u.content, u.meta, u.msgType, u.id,
			); err != nil {
				return report, err
			}
		}
	}
	if opts.InlineImages {
		report.Items = append(report.Items, imageItem)
		report.TotalBytes += imageItem.Bytes
	}
	if opts.HistoricalPayload {
		report.Items = append(report.Items, histItem)
		report.TotalBytes += histItem.Bytes
	}

	if opts.BlankConversations {
		item, err := s.gcBlankConversations(opts.Apply)
		if err != nil {
			return report, err
		}
		report.Items = append(report.Items, item)
		report.TotalBytes += item.Bytes
	}
	if opts.FinishedQueue {
		item, err := s.gcFinishedQueue(opts.Apply)
		if err != nil {
			return report, err
		}
		report.Items = append(report.Items, item)
		report.TotalBytes += item.Bytes
	}
	if opts.StaleSummaries {
		item, err := s.gcStaleSummaries(opts.Apply)
		if err != nil {
			return report, err
		}
		report.Items = append(report.Items, item)
		report.TotalBytes += item.Bytes
	}
	if opts.Vacuum {
		item := StorageGCItem{
			ID:     "vacuum",
			Label:  "压缩数据库文件",
			Detail: "重建全文索引并 VACUUM，把前面删掉的空间从文件里释放。空闲页本身不大，不改写正文时几乎缩不了文件。",
		}
		if opts.Apply {
			if err := s.rebuildSearchAndVacuum(); err != nil {
				report.VacuumError = err.Error()
			} else {
				report.Vacuumed = true
				item.Count = 1
			}
		}
		report.Items = append(report.Items, item)
	}
	return report, nil
}

func rewriteMessagePayload(role, content, meta string, msgType int, protected bool, opts StorageGCOptions, ext *imageExternalizer) (string, string, int, int, int, bool) {
	origContent, origMeta, origType := content, meta, msgType
	if opts.InlineImages {
		if mime, _, ok := decodeImagePayload(content); ok {
			if next, ok := ext.rewriteImageString(content, true); ok && next != content {
				content = next
				if msgType == llm.MsgTypeText {
					msgType = llm.MsgTypeImage
				}
				meta = ensureImageMeta(meta, mime)
			}
		} else {
			content = ext.rewriteEmbedded(content)
		}
		if meta != "" {
			meta = rewriteMetadata(meta, ext, true, false)
		}
	}
	imageSaved := positiveShrink(origContent, content) + positiveShrink(origMeta, meta)

	midContent, midMeta := content, meta
	compact := opts.HistoricalPayload && !protected
	if compact && isToolPayload(role, msgType) && len(content) > storageGCToolKeep {
		content = trimToolOutput(content)
	}
	if compact && meta != "" {
		meta = rewriteMetadata(meta, ext, false, true)
	}
	histSaved := 0
	if compact {
		histSaved = positiveShrink(midContent, content) + positiveShrink(midMeta, meta)
	}
	changed := content != origContent || meta != origMeta || msgType != origType
	return content, meta, msgType, imageSaved, histSaved, changed
}

func trimToolOutput(s string) string {
	if len(s) <= storageGCToolKeep {
		return s
	}
	cut := storageGCToolKeep
	for cut > 0 && cut < len(s) && s[cut]&0xC0 == 0x80 {
		cut--
	}
	return s[:cut] + storageGCToolMark
}

func positiveShrink(before, after string) int {
	if len(after) >= len(before) {
		return 0
	}
	return len(before) - len(after)
}

func isToolPayload(role string, msgType int) bool {
	return role == "tool" || msgType == llm.MsgTypeTool || msgType == llm.MsgTypeCommand
}

func ensureImageMeta(meta, mime string) string {
	obj := map[string]any{}
	if meta != "" {
		_ = json.Unmarshal([]byte(meta), &obj)
	}
	if _, ok := obj["type"]; !ok {
		obj["type"] = llm.TypeImage
	}
	if _, ok := obj["mime_type"]; !ok && mime != "" {
		obj["mime_type"] = mime
	}
	b, err := json.Marshal(obj)
	if err != nil {
		return meta
	}
	return string(b)
}

func rewriteMetadata(meta string, ext *imageExternalizer, images, compact bool) string {
	var obj map[string]any
	if err := json.Unmarshal([]byte(meta), &obj); err != nil {
		if !images {
			return meta
		}
		return ext.rewriteEmbedded(meta)
	}
	changed := false
	if raw, ok := obj["parts"]; ok {
		next, ch := transformPartsValue(raw, ext, images, compact)
		if ch {
			obj["parts"] = next
			changed = true
		}
	}
	if images {
		for k, v := range obj {
			if k == "parts" {
				continue
			}
			s, ok := v.(string)
			if !ok {
				continue
			}
			next := ext.rewriteEmbedded(s)
			if whole, ok := ext.rewriteImageString(s, false); ok {
				next = whole
			}
			if next != s {
				obj[k] = next
				changed = true
			}
		}
	}
	if !changed {
		return meta
	}
	b, err := json.Marshal(obj)
	if err != nil {
		return meta
	}
	return string(b)
}

func transformPartsValue(raw any, ext *imageExternalizer, images, compact bool) (any, bool) {
	switch v := raw.(type) {
	case string:
		var parts []map[string]any
		if err := json.Unmarshal([]byte(v), &parts); err != nil {
			if !images {
				return raw, false
			}
			next := ext.rewriteEmbedded(v)
			return next, next != v
		}
		next, changed := editParts(parts, ext, images, compact)
		if !changed {
			return raw, false
		}
		b, err := json.Marshal(next)
		if err != nil {
			return raw, false
		}
		return string(b), true
	case []any:
		parts := make([]map[string]any, 0, len(v))
		for _, item := range v {
			m, ok := item.(map[string]any)
			if !ok {
				return raw, false
			}
			parts = append(parts, m)
		}
		next, changed := editParts(parts, ext, images, compact)
		if !changed {
			return raw, false
		}
		return next, true
	default:
		return raw, false
	}
}

func editParts(parts []map[string]any, ext *imageExternalizer, images, compact bool) ([]map[string]any, bool) {
	out := make([]map[string]any, 0, len(parts))
	changed := false
	for _, p := range parts {
		kind, _ := p["kind"].(string)
		if compact && kind == "thinking" {
			changed = true
			continue
		}
		for _, key := range []string{"text", "result", "args"} {
			s, ok := p[key].(string)
			if !ok || s == "" {
				continue
			}
			next := s
			if images {
				next = ext.rewriteEmbedded(next)
				if whole, ok := ext.rewriteImageString(next, false); ok {
					next = whole
				}
			}
			if compact && (key == "result" || (key == "text" && kind == "tool")) && len(next) > storageGCToolKeep {
				next = trimToolOutput(next)
			}
			if next != s {
				p[key] = next
				changed = true
			}
		}
		if nested, ok := p["parts"]; ok {
			next, ch := transformPartsValue(nested, ext, images, compact)
			if ch {
				p["parts"] = next
				changed = true
			}
		}
		out = append(out, p)
	}
	return out, changed
}

func (s *Store) recentMessageIDs(keep int) (map[int64]struct{}, error) {
	rows, err := s.db.Query(`
		SELECT id FROM (
			SELECT id, ROW_NUMBER() OVER (PARTITION BY conversation_id ORDER BY seq DESC, id DESC) AS rn
			FROM messages
		) WHERE rn <= ?`, keep)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]struct{}{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = struct{}{}
	}
	return out, rows.Err()
}

func (s *Store) gcBlankConversations(apply bool) (StorageGCItem, error) {
	item := StorageGCItem{
		ID:     "blank_conversations",
		Label:  "没有消息的空白对话",
		Detail: "删除超过 24 小时、没有任何消息、也没有排队回合的会话。标题会一起去掉。有消息的对话，以及刚开的空白会话，都会留下。",
	}
	cutoff := time.Now().Add(-StorageGCBlankGrace).Unix()
	rows, err := s.db.Query(`
		SELECT c.id, length(COALESCE(c.title,'')) + length(COALESCE(c.metadata,''))
		FROM conversations c
		WHERE NOT EXISTS (SELECT 1 FROM messages m WHERE m.conversation_id = c.id)
		  AND NOT EXISTS (
		        SELECT 1 FROM turn_queue t
		         WHERE t.session_id = c.id AND t.status IN (?, ?)
		  )
		  AND c.updated_at < ?`, TurnQueueStatusQueued, TurnQueueStatusRunning, cutoff)
	if err != nil {
		return item, err
	}
	defer rows.Close()
	type blank struct {
		id    string
		bytes int64
	}
	var blanks []blank
	for rows.Next() {
		var b blank
		if err := rows.Scan(&b.id, &b.bytes); err != nil {
			return item, err
		}
		blanks = append(blanks, b)
		item.Count++
		item.Bytes += b.bytes
	}
	if err := rows.Err(); err != nil {
		return item, err
	}
	if !apply {
		return item, nil
	}
	for _, b := range blanks {
		if _, err := s.db.Exec(`DELETE FROM todo_items WHERE session_id = ?`, b.id); err != nil {
			return item, err
		}
		if _, err := s.db.Exec(`DELETE FROM message_bookmarks WHERE conversation_id = ?`, b.id); err != nil {
			return item, err
		}
		if _, err := s.db.Exec(`DELETE FROM turn_changes WHERE conversation_id = ?`, b.id); err != nil {
			return item, err
		}
		if _, err := s.db.Exec(`DELETE FROM conversations WHERE id = ?`, b.id); err != nil {
			return item, err
		}
		if s.currentID == b.id {
			s.currentID = ""
		}
	}
	return item, nil
}

func (s *Store) gcFinishedQueue(apply bool) (StorageGCItem, error) {
	item := StorageGCItem{
		ID:     "finished_queue",
		Label:  "已结束的发送队列",
		Detail: "删除状态为完成或失败的排队回合。排队中和正在发送的保留。",
	}
	var n int
	var bytes sql.NullInt64
	err := s.db.QueryRow(`
		SELECT COUNT(*), COALESCE(SUM(length(message) + length(payload_json)), 0)
		FROM turn_queue WHERE status IN (?, ?)`,
		TurnQueueStatusDone, TurnQueueStatusFailed,
	).Scan(&n, &bytes)
	if err != nil {
		return item, err
	}
	item.Count = n
	if bytes.Valid {
		item.Bytes = bytes.Int64
	}
	if apply && n > 0 {
		if _, err := s.db.Exec(
			`DELETE FROM turn_queue WHERE status IN (?, ?)`,
			TurnQueueStatusDone, TurnQueueStatusFailed,
		); err != nil {
			return item, err
		}
	}
	return item, nil
}

func (s *Store) gcStaleSummaries(apply bool) (StorageGCItem, error) {
	item := StorageGCItem{
		ID:     "stale_summaries",
		Label:  "对不上消息的旧摘要",
		Detail: "删除范围内已经没有消息的压缩摘要。仍覆盖现有消息的摘要保留，模型上下文不受影响。",
	}
	q := `
		SELECT COUNT(*), COALESCE(SUM(length(summary)), 0)
		FROM summaries s
		WHERE NOT EXISTS (
			SELECT 1 FROM messages m
			 WHERE m.conversation_id = s.conversation_id
			   AND m.id BETWEEN s.range_start AND s.range_end
		)`
	var n int
	var bytes sql.NullInt64
	if err := s.db.QueryRow(q).Scan(&n, &bytes); err != nil {
		return item, err
	}
	item.Count = n
	if bytes.Valid {
		item.Bytes = bytes.Int64
	}
	if apply && n > 0 {
		if _, err := s.db.Exec(`
			DELETE FROM summaries
			 WHERE NOT EXISTS (
			    SELECT 1 FROM messages m
			     WHERE m.conversation_id = summaries.conversation_id
			       AND m.id BETWEEN summaries.range_start AND summaries.range_end
			 )`); err != nil {
			return item, err
		}
	}
	return item, nil
}

func (s *Store) rebuildSearchAndVacuum() error {
	if _, err := s.db.Exec(`INSERT INTO messages_fts(messages_fts) VALUES('rebuild')`); err != nil {
		return fmt.Errorf("rebuild search index: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.db.SetMaxOpenConns(1)
	defer s.db.SetMaxOpenConns(4)
	if _, err := s.db.Exec(`VACUUM`); err != nil {
		return fmt.Errorf("vacuum: %w", err)
	}
	return nil
}
