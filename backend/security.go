package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

type riskRule struct {
	severity string
	title    string
	pattern  *regexp.Regexp
}

var riskRules = []riskRule{
	{"critical", "下载后直接执行远程脚本", regexp.MustCompile(`(?i)\b(?:curl|wget)\b[^\n|]{0,240}\|\s*(?:sudo\s+)?(?:ba|z|k)?sh\b`)},
	{"critical", "PowerShell 编码命令", regexp.MustCompile(`(?i)powershell(?:\.exe)?[^\n]{0,160}(?:-enc|-encodedcommand)\b`)},
	{"critical", "破坏性删除根目录", regexp.MustCompile(`(?i)\brm\s+-rf\s+/(?:\s|$|\*)`)},
	{"critical", "疑似 Fork 炸弹", regexp.MustCompile(`:\(\)\s*\{\s*:\|:&\s*\};:`)},
	{"high", "使用 sudo 提权", regexp.MustCompile(`(?i)\bsudo\s+`)},
	{"high", "读取 SSH 私钥或 known_hosts", regexp.MustCompile(`(?i)(?:~|\$HOME|/home/[^/\s]+)/\.ssh\b|\bid_(?:rsa|ed25519|ecdsa)\b`)},
	{"high", "读取云凭据", regexp.MustCompile(`(?i)(?:~|\$HOME)/\.aws/(?:credentials|config)|\.aws/credentials`)},
	{"high", "读取 .env 环境变量", regexp.MustCompile(`(?i)\b(?:cat|type|Get-Content)\b[^\n]{0,100}\.env\b`)},
	{"high", "Base64 解码后执行", regexp.MustCompile(`(?i)\bbase64\s+(?:-d|--decode)\b`)},
	{"high", "动态执行系统命令", regexp.MustCompile(`(?i)\bos\.system\s*\(|subprocess\.(?:run|Popen)\s*\([^\n]{0,120}shell\s*=\s*True|child_process|execSync\s*\(`)},
	{"high", "修改文件执行权限", regexp.MustCompile(`(?i)\bchmod\s+\+x\b`)},
	{"high", "读取系统钥匙串", regexp.MustCompile(`(?i)\bsecurity\s+find-(?:generic|internet)-password\b`)},
	{"medium", "发起网络请求", regexp.MustCompile(`(?i)\b(?:curl|wget)\s+|Invoke-WebRequest\b`)},
	{"medium", "全局安装依赖", regexp.MustCompile(`(?i)\bnpm\s+install\s+-g\b|\bpip\s+install\b`)},
	{"medium", "远程主机操作", regexp.MustCompile(`(?i)\b(?:ssh|scp|rsync)\s+`)},
	{"medium", "调用容器或虚拟化", regexp.MustCompile(`(?i)\bdocker\s+(?:run|exec)\b`)},
}

func scanRisk(root string, files []FileEntry) []Finding {
	var findings []Finding
	seen := map[string]bool{}
	for _, file := range files {
		if len(findings) >= 40 {
			break
		}
		path := filepath.Join(root, filepath.FromSlash(file.Path))
		if file.Binary || !isScannable(path) {
			continue
		}
		info, err := os.Stat(path)
		if err != nil || info.Size() > 1024*1024 {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		text := string(data)
		lines := strings.Split(text, "\n")
		for _, rule := range riskRules {
			for index, line := range lines {
				if len(line) > 4000 {
					line = line[:4000]
				}
				if !rule.pattern.MatchString(line) {
					continue
				}
				snippet := sanitizeLabel(strings.TrimSpace(line), 180)
				key := rule.title + "\x00" + file.Path + "\x00" + snippet
				if seen[key] {
					continue
				}
				seen[key] = true
				findings = append(findings, Finding{
					Severity: rule.severity, Title: rule.title,
					File: file.Path, Line: index + 1, Snippet: snippet,
				})
				if len(findings) >= 40 {
					break
				}
			}
		}
	}
	return findings
}

func isScannable(path string) bool {
	base := strings.ToLower(filepath.Base(path))
	switch filepath.Ext(base) {
	case ".md", ".sh", ".bash", ".zsh", ".fish", ".ps1", ".bat", ".cmd", ".py", ".js", ".mjs", ".cjs", ".ts", ".rb", ".go", ".pl", ".php", ".yaml", ".yml", ".json", ".toml":
		return true
	}
	if filepath.Ext(base) == "" {
		file, err := os.Open(path)
		if err != nil {
			return false
		}
		defer file.Close()
		head := make([]byte, 128)
		n, _ := file.Read(head)
		return strings.HasPrefix(string(head[:n]), "#!")
	}
	return false
}

func hasCriticalRisk(findings []Finding) bool {
	for _, finding := range findings {
		if finding.Severity == "critical" {
			return true
		}
	}
	return false
}
