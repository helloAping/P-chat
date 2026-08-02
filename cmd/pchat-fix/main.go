// pchat-fix 是一个一次性修复工具：清理 store.db 里 summaries 表的
// 损坏区间记录。
//
// 背景：2026-08 的"发消息 → 内存飙升 / 对话卡住"事故根因是 summaries
// 表里混入了跨消息 ID 方案的坏区间（start 取自旧的自增小 ID、end 取自
// 前端生成的微秒时间戳，跨度 ~1.78e15）。旧的 Compress 会把区间逐值展开
// 成 id→bool map，O(跨度) 内存直接打爆进程堆。
//
// 新版本 (migration v10) 在打开数据库时自动清理这些坏区间。本工具用于
// 给存量设备手动触发一次修复，或只做诊断（-check）不改数据。
//
// 用法：
//
//	go run ./cmd/pchat-fix -check                # 只诊断，不写库
//	go run ./cmd/pchat-fix                       # 备份后修复
//	go run ./cmd/pchat-fix -db <path>            # 指定库路径（默认 paths.MemoryDB()）
package main

import (
	"database/sql"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"

	"github.com/p-chat/pchat/internal/memory"
	"github.com/p-chat/pchat/internal/paths"
)

// corruptRangeThreshold 与 migration v10 的清理阈值一致：正常批量最多
// 压缩 100 条连续消息，单套 ID 方案下跨度不会超过 ~10^8；超过 10^12
// 只可能是跨两套 ID 方案的坏区间。
const corruptRangeThreshold = 1000000000000

const countSQL = `
SELECT COUNT(*), COALESCE(MAX(range_end - range_start + 1), 0)
FROM summaries WHERE range_end - range_start + 1 > ?`

func main() {
	log.SetFlags(0)

	dbPath := flag.String("db", "", "store.db 路径（默认自动定位 ~/.p-chat/memory/store.db）")
	check := flag.Bool("check", false, "只诊断，不修改数据库")
	flag.Parse()

	target := *dbPath
	if target == "" {
		target = paths.MemoryDB()
	}

	if _, err := os.Stat(target); err != nil {
		log.Fatalf("数据库不存在: %s (%v)", target, err)
	}

	if *check {
		if err := runCheck(target); err != nil {
			log.Fatalf("诊断失败: %v", err)
		}
		return
	}

	if err := runFix(target); err != nil {
		log.Fatalf("修复失败: %v", err)
	}
}

// runCheck 打开数据库并统计损坏区间数量，不改写任何数据。
func runCheck(dbPath string) error {
	bad, maxSpan, err := countCorrupt(dbPath)
	if err != nil {
		return err
	}
	fmt.Printf("数据库: %s\n", dbPath)
	fmt.Printf("损坏 summaries 区间: %d 条（最大跨度 %d）\n", bad, maxSpan)
	if bad == 0 {
		fmt.Println("无需修复：未发现跨 ID 方案的坏区间。")
	} else {
		fmt.Println("建议运行: go run ./cmd/pchat-fix  （会先备份再清理）")
	}
	return nil
}

// runFix 先备份数据库文件，再打开 store（触发迁移 v10 自动清理），
// 最后核对清理结果。
func runFix(dbPath string) error {
	bad, _, err := countCorrupt(dbPath)
	if err != nil {
		return err
	}
	fmt.Printf("数据库: %s\n", dbPath)
	if bad == 0 {
		fmt.Println("未发现损坏 summaries 区间，无需修复。")
		return nil
	}
	fmt.Printf("发现 %d 条损坏 summaries 区间，开始备份并修复…\n", bad)

	if err := backupDB(dbPath); err != nil {
		return fmt.Errorf("备份失败: %w", err)
	}

	// OpenAt 会跑全部迁移（含 v10 purge_corrupt_summary_ranges）。
	st, err := memory.OpenAt(dbPath, 50)
	if err != nil {
		return fmt.Errorf("打开数据库: %w", err)
	}
	if err := st.Close(); err != nil {
		return fmt.Errorf("关闭数据库: %w", err)
	}

	after, maxSpan, err := countCorrupt(dbPath)
	if err != nil {
		return err
	}
	fmt.Printf("修复完成：损坏区间 %d -> %d，剩余最大跨度 %d\n", bad, after, maxSpan)
	if after > 0 {
		return fmt.Errorf("仍有 %d 条损坏区间未被清理，请检查迁移是否生效", after)
	}
	return nil
}

// countCorrupt 打开一个独立连接，统计坏区间数量与最大跨度。
func countCorrupt(dbPath string) (count int, maxSpan int64, err error) {
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(dbPath)+"?mode=ro")
	if err != nil {
		return 0, 0, err
	}
	defer db.Close()
	if err := db.QueryRow(countSQL, corruptRangeThreshold).Scan(&count, &maxSpan); err != nil {
		return 0, 0, err
	}
	return count, maxSpan, nil
}

// backupDB 复制数据库文件（连同 WAL/shm 若存在）到同目录的
// .backup-<时间戳> 副本。WAL 模式下直接文件复制可能丢失尚未
// checkpoint 的数据，因此先强制 checkpoint 再复制主文件。
func backupDB(dbPath string) error {
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(dbPath))
	if err != nil {
		return err
	}
	defer db.Close()
	if _, err := db.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		log.Printf("warn: wal_checkpoint 失败（可能无 WAL）: %v", err)
	}
	if err := db.Close(); err != nil {
		return err
	}

	backupPath := dbPath + ".backup-" + time.Now().Format("20060102-150405")
	for _, suffix := range []string{"", "-wal", "-shm"} {
		src := dbPath + suffix
		if _, err := os.Stat(src); err != nil {
			continue
		}
		if err := copyFile(src, backupPath+suffix); err != nil {
			return err
		}
	}
	fmt.Printf("已备份到: %s\n", backupPath)
	return nil
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
