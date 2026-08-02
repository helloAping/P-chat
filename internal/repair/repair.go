// Package repair 提供 summaries 坏区间清理的共享逻辑，供两个入口复用：
//
//   - cmd/pchat-fix —— 独立单文件修复工具（可拷贝到任意机器运行）
//   - pchat-server repair —— 内嵌在 pchat-server 里的修复子命令
//
// 注意：新版本 pchat-server 启动时（memory.OpenAt）会自动跑迁移 v10
// 清理坏区间，本包只在需要"诊断 / 不升级直接修复 / 验证"时使用。
package repair

import (
	"database/sql"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

// CorruptRangeThreshold 与 migration v10 的清理阈值一致：正常批量最多
// 压缩 100 条连续消息，单套 ID 方案（自增小 ID 或前端微秒时间戳）下
// 跨度不会超过 ~10^8；超过 10^12 只可能是跨两套 ID 方案的坏区间。
const CorruptRangeThreshold = 1000000000000

const countSQL = `
SELECT COUNT(*), COALESCE(MAX(range_end - range_start + 1), 0)
FROM summaries WHERE range_end - range_start + 1 > ?`

// Check 打开数据库（只读）统计坏区间数量与最大跨度，不改写任何数据。
func Check(dbPath string) (count int, maxSpan int64, err error) {
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(dbPath)+"?mode=ro")
	if err != nil {
		return 0, 0, err
	}
	defer db.Close()
	if err := db.QueryRow(countSQL, CorruptRangeThreshold).Scan(&count, &maxSpan); err != nil {
		return 0, 0, err
	}
	return count, maxSpan, nil
}

// Fix 先备份数据库，再直接执行与迁移 v10 相同的清理 SQL（不依赖
// schema_migrations 状态——库可能已到 v10 但坏行是之后写入的），最后
// 核对清理结果。返回备份文件路径。
func Fix(dbPath string) (backupPath string, err error) {
	count, _, err := Check(dbPath)
	if err != nil {
		return "", err
	}
	if count == 0 {
		return "", nil
	}
	backupPath, err = backupDB(dbPath)
	if err != nil {
		return "", fmt.Errorf("备份失败: %w", err)
	}

	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(dbPath))
	if err != nil {
		return "", err
	}
	defer db.Close()
	res, err := db.Exec(
		`DELETE FROM summaries WHERE range_end - range_start + 1 > ?`,
		CorruptRangeThreshold,
	)
	if err != nil {
		return "", fmt.Errorf("清理坏区间: %w", err)
	}
	if _, err := res.RowsAffected(); err != nil {
		return "", err
	}

	after, maxSpan, err := Check(dbPath)
	if err != nil {
		return "", err
	}
	if after > 0 {
		return backupPath, fmt.Errorf("仍有 %d 条损坏区间未被清理（剩余最大跨度 %d）", after, maxSpan)
	}
	return backupPath, nil
}

// backupDB 复制数据库文件（连同 WAL/shm 若存在）到同目录的
// .backup-<时间戳> 副本。WAL 模式下直接文件复制可能丢失尚未
// checkpoint 的数据，因此先强制 checkpoint 再复制主文件。
func backupDB(dbPath string) (string, error) {
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(dbPath))
	if err != nil {
		return "", err
	}
	if _, err := db.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		log.Printf("warn: wal_checkpoint 失败（可能无 WAL）: %v", err)
	}
	if err := db.Close(); err != nil {
		return "", err
	}

	backupPath := dbPath + ".backup-" + time.Now().Format("20060102-150405")
	for _, suffix := range []string{"", "-wal", "-shm"} {
		src := dbPath + suffix
		if _, err := os.Stat(src); err != nil {
			continue
		}
		if err := copyFile(src, backupPath+suffix); err != nil {
			return "", err
		}
	}
	return backupPath, nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}
