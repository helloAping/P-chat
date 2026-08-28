package export

import (
	"regexp"
	"strings"
)

var legacyDirCountRe = regexp.MustCompile(`(\d+)\s+��Ŀ¼`)

func normalizeExportText(s string) string {
	if s == "" || (!strings.ContainsRune(s, '\uFFFD') && !strings.Contains(s, "ϵͳ") && !strings.Contains(s, "��")) {
		return s
	}
	s = legacyDirCountRe.ReplaceAllString(s, "${1} 个目录")
	s = strings.ReplaceAll(s, " ��Ŀ¼", " 的目录")
	replacements := []struct {
		old string
		new string
	}{
		{"ϵͳ�Ҳ���ָ����·����", "系统找不到指定的路径。"},
		{"�еľ���", "中的卷是"},
		{"�������к���", "卷的序列号是"},
		{"������", "驱动器"},
		{"���ļ�", "个文件"},
		{"�����ֽ�", "可用字节"},
		{"�ֽ�", "字节"},
	}
	for _, repl := range replacements {
		s = strings.ReplaceAll(s, repl.old, repl.new)
	}
	return s
}
