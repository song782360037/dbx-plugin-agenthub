package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
	"unicode/utf8"
)

func nowString() string {
	return time.Now().UTC().Format(time.RFC3339)
}

func randomID(size int) string {
	buf := make([]byte, size)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf)
}

func expandHome(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if path == "~" || strings.HasPrefix(path, "~/") || strings.HasPrefix(path, `~\`) {
		home, err := os.UserHomeDir()
		if err == nil {
			if len(path) <= 2 {
				return home
			}
			return filepath.Join(home, path[2:])
		}
	}
	return path
}

func absoluteClean(path string) (string, error) {
	path = expandHome(path)
	if path == "" {
		return "", errors.New("路径不能为空")
	}
	if !filepath.IsAbs(path) {
		abs, err := filepath.Abs(path)
		if err != nil {
			return "", err
		}
		path = abs
	}
	return filepath.Clean(path), nil
}

func existingDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func withinRoot(root, target string) bool {
	root = filepath.Clean(root)
	target = filepath.Clean(target)
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

func samePath(left, right string) bool {
	left = filepath.Clean(left)
	right = filepath.Clean(right)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(left, right)
	}
	return left == right
}

// detectDirLink catches both symlinks and Windows junctions. Go does not mark
// junctions as ModeSymlink on Windows, but os.Readlink still resolves them.
func detectDirLink(path string) (string, bool) {
	target, err := os.Readlink(path)
	if err != nil || strings.TrimSpace(target) == "" {
		return "", false
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(filepath.Dir(path), target)
	}
	if resolved, err := filepath.EvalSymlinks(target); err == nil {
		target = resolved
	}
	return filepath.Clean(target), true
}

func safeJoin(root, rel string) (string, error) {
	rel = strings.TrimSpace(rel)
	if rel == "" {
		return "", errors.New("相对路径不能为空")
	}
	rel = strings.ReplaceAll(rel, "\\", "/")
	if strings.HasPrefix(rel, "/") || filepath.IsAbs(rel) || (len(rel) >= 2 && rel[1] == ':') {
		return "", errors.New("不允许绝对路径")
	}
	cleaned := filepath.Clean(filepath.FromSlash(rel))
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return "", errors.New("路径不能包含 ..")
	}
	target := filepath.Join(root, cleaned)
	if !withinRoot(root, target) {
		return "", errors.New("路径超出允许范围")
	}
	return target, nil
}

func relSlash(root, target string) (string, error) {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return "", err
	}
	return filepath.ToSlash(rel), nil
}

func isBinaryFile(path string) bool {
	file, err := os.Open(path)
	if err != nil {
		return false
	}
	defer file.Close()
	buf := make([]byte, 8000)
	n, _ := file.Read(buf)
	if n == 0 {
		return false
	}
	if bytesContainsNUL(buf[:n]) {
		return true
	}
	return !utf8.Valid(buf[:n])
}

func bytesContainsNUL(buf []byte) bool {
	for _, b := range buf {
		if b == 0 {
			return true
		}
	}
	return false
}

func hashBytes(buf []byte) string {
	sum := sha256.Sum256(buf)
	return hex.EncodeToString(sum[:])
}

type sha256Writer struct {
	writer io.Writer
	hash   hash.Hash
}

func newSHA256Writer(writer io.Writer) *sha256Writer {
	return &sha256Writer{writer: writer, hash: sha256.New()}
}

func (writer *sha256Writer) Write(data []byte) (int, error) {
	if _, err := writer.hash.Write(data); err != nil {
		return 0, err
	}
	return writer.writer.Write(data)
}

func (writer *sha256Writer) Hex() string {
	return hex.EncodeToString(writer.hash.Sum(nil))
}

func hashFile(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func atomicWriteFile(path string, data []byte, mode fs.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	temp, err := os.CreateTemp(dir, ".tmp-"+filepath.Base(path)+"-*")
	if err != nil {
		return err
	}
	tempName := temp.Name()
	defer os.Remove(tempName)
	if err := temp.Chmod(mode); err != nil && runtime.GOOS != "windows" {
		temp.Close()
		return err
	}
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(tempName, path)
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

type copyLimits struct {
	MaxFiles int
	MaxBytes int64
}

// copyTree copies skill content while excluding generated directories.
func copyTree(src, dst string, limits copyLimits, skipMarkers bool) ([]FileEntry, int64, error) {
	return copyTreeFiltered(src, dst, limits, skipMarkers, true)
}

// copyTreeFiltered also supports complete backups without import filters.
func copyTreeFiltered(src, dst string, limits copyLimits, skipMarkers, skipDirs bool) ([]FileEntry, int64, error) {
	src = filepath.Clean(src)
	var files []FileEntry
	var total int64
	count := 0
	err := filepath.WalkDir(src, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == src {
			return os.MkdirAll(dst, 0o755)
		}
		rel, err := relSlash(src, path)
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if skipDirs && shouldSkipDir(entry.Name()) {
				return filepath.SkipDir
			}
			return os.MkdirAll(filepath.Join(dst, filepath.FromSlash(rel)), 0o755)
		}
		if skipMarkers && isMarkerName(entry.Name()) {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("技能包含符号链接，已拒绝：%s", rel)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("技能包含不支持的文件类型：%s", rel)
		}
		count++
		if limits.MaxFiles > 0 && count > limits.MaxFiles {
			return fmt.Errorf("文件数量超过限制（最多 %d 个）", limits.MaxFiles)
		}
		total += info.Size()
		if limits.MaxBytes > 0 && total > limits.MaxBytes {
			return fmt.Errorf("技能体积超过限制（最多 %d MiB）", limits.MaxBytes/1024/1024)
		}
		if err := copyFile(path, filepath.Join(dst, filepath.FromSlash(rel))); err != nil {
			return err
		}
		files = append(files, FileEntry{Path: rel, Size: info.Size(), Executable: isExecutable(info.Mode())})
		return nil
	})
	return files, total, err
}

func shouldSkipDir(name string) bool {
	switch strings.ToLower(name) {
	case ".git", ".hg", ".svn", "node_modules", ".venv", "venv", "__pycache__", ".idea", ".vscode":
		return true
	}
	return false
}

func isExecutable(mode fs.FileMode) bool {
	return runtime.GOOS != "windows" && mode&0o111 != 0
}

func isMarkerName(name string) bool {
	switch name {
	case markerName, legacyMarkerName, ".ccgui-managed.json", ".skills-manager-source.json", "_meta.json", "_skillhub_meta.json":
		return true
	}
	return false
}

func jsonMarshalIndent(value any) ([]byte, error) {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

func writeJSONFile(path string, value any) error {
	data, err := jsonMarshalIndent(value)
	if err != nil {
		return err
	}
	return atomicWriteFile(path, data, 0o600)
}

func sanitizeLabel(value string, max int) string {
	value = strings.TrimSpace(strings.ReplaceAll(value, "\r", " "))
	value = strings.ReplaceAll(value, "\n", " ")
	if max > 0 && len(value) > max {
		value = value[:max]
	}
	return value
}

func uniqueStrings(values []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

func pathHasSkillMD(dir string) bool {
	info, err := os.Stat(filepath.Join(dir, "SKILL.md"))
	return err == nil && info.Mode().IsRegular()
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}
