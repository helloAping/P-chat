package main

import (
	"fmt"
	"os"

	"github.com/p-chat/pchat/internal/paths"
	"github.com/p-chat/pchat/internal/repair"
	"github.com/spf13/cobra"
)

// repairCmd 内嵌的修复子命令：诊断 / 修复 summaries 坏区间。
//
// 需要给没有 Go 工具链的机器修复时，直接用编译好的独立工具
// pchat-fix.exe 即可；此子命令提供同样的能力，方便在已装
// pchat-server 的机器上用现成二进制处理。
//
//	用法：
//	  pchat-server repair -check          # 只诊断
//	  pchat-server repair -db <path>      # 指定库路径
//	  pchat-server repair                 # 备份后修复
var repairCmd = &cobra.Command{
	Use:   "repair",
	Short: "修复 store.db 里的损坏 summaries 区间",
	Long: `清理 store.db 中跨消息 ID 方案的损坏 summaries 区间。
该问题会导致发消息时内存飙升 / 对话卡住。`,
	Args: cobra.NoArgs,
	RunE: runRepair,
}

var (
	repairDB    string
	repairCheck bool
)

func init() {
	repairCmd.Flags().StringVar(&repairDB, "db", "", "store.db 路径（默认自动定位 ~/.p-chat/memory/store.db）")
	repairCmd.Flags().BoolVar(&repairCheck, "check", false, "只诊断，不修改数据库")
	rootCmd.AddCommand(repairCmd)
}

func runRepair(cmd *cobra.Command, args []string) error {
	target := repairDB
	if target == "" {
		target = paths.MemoryDB()
	}
	if _, err := os.Stat(target); err != nil {
		return fmt.Errorf("数据库不存在: %s (%w)", target, err)
	}

	count, maxSpan, err := repair.Check(target)
	if err != nil {
		return fmt.Errorf("打开数据库失败: %w", err)
	}

	if repairCheck {
		fmt.Printf("数据库: %s\n", target)
		fmt.Printf("损坏 summaries 区间: %d 条（最大跨度 %d）\n", count, maxSpan)
		if count == 0 {
			fmt.Println("无需修复：未发现跨 ID 方案的坏区间。")
		} else {
			fmt.Println("建议运行: pchat-server repair  （会先备份再清理）")
		}
		return nil
	}

	if count == 0 {
		fmt.Printf("数据库: %s\n", target)
		fmt.Println("未发现损坏 summaries 区间，无需修复。")
		return nil
	}
	fmt.Printf("数据库: %s\n", target)
	fmt.Printf("发现 %d 条损坏 summaries 区间，开始备份并修复…\n", count)
	backupPath, err := repair.Fix(target)
	if err != nil {
		return fmt.Errorf("修复失败: %w", err)
	}
	fmt.Printf("修复完成。备份: %s\n", backupPath)
	return nil
}
