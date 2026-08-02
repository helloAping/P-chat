// pchat-fix 是一个独立的 summaries 坏区间修复工具。编译成单个 exe，
// 可拷贝到没有 Go 工具链的机器上运行。
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
//	pchat-fix.exe -check                # 只诊断，不写库
//	pchat-fix.exe                       # 备份后修复
//	pchat-fix.exe -db <path>            # 指定库路径（默认自动定位 ~/.p-chat/memory/store.db）
//
// 构建（产出 bin/pchat-fix.exe，纯静态、无前端依赖）：
//
//	go build -o bin/pchat-fix.exe ./cmd/pchat-fix
package main

import (
	"flag"
	"fmt"
	"log"

	"github.com/p-chat/pchat/internal/paths"
	"github.com/p-chat/pchat/internal/repair"
)

func main() {
	log.SetFlags(0)

	dbPath := flag.String("db", "", "store.db 路径（默认自动定位 ~/.p-chat/memory/store.db）")
	check := flag.Bool("check", false, "只诊断，不修改数据库")
	flag.Parse()

	target := *dbPath
	if target == "" {
		target = paths.MemoryDB()
	}

	count, maxSpan, err := repair.Check(target)
	if err != nil {
		log.Fatalf("打开数据库失败: %v", err)
	}

	if *check {
		fmt.Printf("数据库: %s\n", target)
		fmt.Printf("损坏 summaries 区间: %d 条（最大跨度 %d）\n", count, maxSpan)
		if count == 0 {
			fmt.Println("无需修复：未发现跨 ID 方案的坏区间。")
		} else {
			fmt.Println("建议运行: pchat-fix.exe  （会先备份再清理）")
		}
		return
	}

	if count == 0 {
		fmt.Printf("数据库: %s\n", target)
		fmt.Println("未发现损坏 summaries 区间，无需修复。")
		return
	}
	fmt.Printf("数据库: %s\n", target)
	fmt.Printf("发现 %d 条损坏 summaries 区间，开始备份并修复…\n", count)
	backupPath, err := repair.Fix(target)
	if err != nil {
		log.Fatalf("修复失败: %v", err)
	}
	fmt.Printf("修复完成。备份: %s\n", backupPath)
}
