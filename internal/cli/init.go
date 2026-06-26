package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/fatih/color"
	"github.com/p-chat/pchat/internal/rules"
	"github.com/p-chat/pchat/internal/skill"
)

func RunInit() error {
	cwd, _ := os.Getwd()
	pchatDir := filepath.Join(cwd, ".p-chat")

	dirs := []string{
		pchatDir,
		filepath.Join(pchatDir, "skills"),
		filepath.Join(pchatDir, "rules"),
	}

	for _, d := range dirs {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return fmt.Errorf("create dir %s: %w", d, err)
		}
	}

	configPath := filepath.Join(pchatDir, "config.yaml")
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		defaultConfig := `# P-Chat 项目配置
# 此配置会覆盖 ~/.p-chat/config.yaml 中的同名项

# LLM:
#   default: "openai"
#   providers:
#     - name: "openai"
#       api_key: "sk-xxx"

# Style:
#   default: "tech"
`
		os.WriteFile(configPath, []byte(defaultConfig), 0o644)
	}

	agentsPath := filepath.Join(cwd, "AGENTS.md")
	if _, err := os.Stat(agentsPath); os.IsNotExist(err) {
		defaultAgents := `# Project Agent Instructions

## 项目概述

（在此描述你的项目）

## 编码规范

（在此定义编码规范）

## 注意事项

（在此列出注意事项）
`
		os.WriteFile(agentsPath, []byte(defaultAgents), 0o644)
	}

	color.Green("  ✓ 已初始化 .p-chat/ 目录结构")
	fmt.Printf("    %s/\n", pchatDir)
	fmt.Println("    ├── config.yaml")
	fmt.Println("    ├── skills/")
	fmt.Println("    └── rules/")
	fmt.Printf("    %s\n", agentsPath)
	return nil
}

func RunSkillsList() error {
	skills, err := skill.LoadAll()
	if err != nil {
		return err
	}

	if len(skills) == 0 {
		color.HiBlack("  暂无已安装技能")
		fmt.Println("  将 SKILL.md 放入 ~/.p-chat/skills/<name>/ 或 .p-chat/skills/<name>/")
		return nil
	}

	color.Cyan("  已安装技能 (%d)\n", len(skills))
	for _, s := range skills {
		fmt.Printf("    • %s\n", s.Name)
		if s.Description != "" {
			color.HiBlack("      %s\n", s.Description)
		}
	}
	return nil
}

func RunRulesList() error {
	rulesList, err := rules.LoadAll()
	if err != nil {
		return err
	}

	if len(rulesList) == 0 {
		color.HiBlack("  暂无已加载规则")
		fmt.Println("  将 .md 文件放入 ~/.p-chat/rules/ 或 .p-chat/rules/")
		return nil
	}

	color.Cyan("  已加载规则 (%d)\n", len(rulesList))
	for _, rule := range rulesList {
		fmt.Printf("    • %s\n", rule.Name)
	}
	return nil
}
