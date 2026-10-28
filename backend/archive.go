package main

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

type archiveLimits struct {
	MaxFiles     int
	MaxBytes     int64
	MaxFileBytes int64
}

func limitsFor(settings Settings) archiveLimits {
	maxMB := settings.MaxDownloadMB
	if maxMB <= 0 || maxMB > 512 {
		maxMB = 50
	}
	maxBytes := int64(maxMB) * 1024 * 1024
	return archiveLimits{MaxFiles: 8000, MaxBytes: maxBytes, MaxFileBytes: 64 * 1024 * 1024}
}

func detectArchive(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	head := make([]byte, 4)
	n, _ := file.Read(head)
	head = head[:n]
	if len(head) >= 4 && string(head[:4]) == "PK\x03\x04" {
		return "zip", nil
	}
	if len(head) >= 2 && head[0] == 0x1f && head[1] == 0x8b {
		return "tar.gz", nil
	}
	return "", errors.New("不是可识别的 zip 或 tar.gz 压缩包")
}

func extractArchive(path, dest string, limits archiveLimits) (string, error) {
	root, err := extractArchiveRaw(path, dest, limits)
	if err != nil {
		return "", err
	}
	return collapseSingleRoot(root), nil
}

func extractArchiveRaw(path, dest string, limits archiveLimits) (string, error) {
	kind, err := detectArchive(path)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return "", err
	}
	switch kind {
	case "zip":
		err = extractZip(path, dest, limits)
	case "tar.gz":
		err = extractTarGz(path, dest, limits)
	}
	if err != nil {
		return "", err
	}
	return dest, nil
}

func collapseSingleRoot(dest string) string {
	entries, err := os.ReadDir(dest)
	if err != nil || len(entries) != 1 || !entries[0].IsDir() {
		return dest
	}
	child := filepath.Join(dest, entries[0].Name())
	childEntries, err := os.ReadDir(child)
	if err != nil {
		return dest
	}
	for _, entry := range childEntries {
		if entry.IsDir() && !strings.HasPrefix(entry.Name(), ".") {
			return child
		}
	}
	if pathHasSkillMD(child) {
		return child
	}
	if len(childEntries) > 0 {
		return child
	}
	return dest
}

func extractZip(path, dest string, limits archiveLimits) error {
	reader, err := zip.OpenReader(path)
	if err != nil {
		return fmt.Errorf("无法打开 zip：%w", err)
	}
	defer reader.Close()
	if limits.MaxFiles > 0 && len(reader.File) > limits.MaxFiles {
		return fmt.Errorf("压缩包文件数超过限制（%d）", limits.MaxFiles)
	}
	var total int64
	for _, file := range reader.File {
		rel, err := safeArchivePath(file.Name)
		if err != nil {
			return fmt.Errorf("压缩包包含不安全路径 %q：%w", file.Name, err)
		}
		if file.FileInfo().Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("压缩包包含符号链接 %q，已拒绝", file.Name)
		}
		if file.FileInfo().IsDir() {
			if err := os.MkdirAll(filepath.Join(dest, rel), 0o755); err != nil {
				return err
			}
			continue
		}
		if !file.Mode().IsRegular() {
			return fmt.Errorf("压缩包包含不支持的文件类型 %q", file.Name)
		}
		size := int64(file.UncompressedSize64)
		if limits.MaxFileBytes > 0 && size > limits.MaxFileBytes {
			return fmt.Errorf("压缩包内文件 %q 超过单文件限制", file.Name)
		}
		total += size
		if limits.MaxBytes > 0 && total > limits.MaxBytes {
			return fmt.Errorf("压缩包解压后体积超过限制（%d MiB）", limits.MaxBytes/1024/1024)
		}
		if file.CompressedSize64 > 0 && file.UncompressedSize64 > file.CompressedSize64*300+1024*1024 {
			return fmt.Errorf("压缩包内文件 %q 压缩比异常，已拒绝", file.Name)
		}
		if err := writeArchiveFile(file, filepath.Join(dest, rel), limits, size); err != nil {
			return err
		}
	}
	return nil
}

func writeArchiveFile(file *zip.File, target string, limits archiveLimits, expected int64) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	perm := file.Mode().Perm() & 0o777
	if perm == 0 {
		perm = 0o644
	}
	out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, perm)
	if err != nil {
		return err
	}
	reader, err := file.Open()
	if err != nil {
		out.Close()
		return err
	}
	_, copyErr := copyLimited(out, reader, limits.MaxFileBytes)
	closeErr := out.Close()
	reader.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	info, err := os.Stat(target)
	if err == nil && expected > 0 && info.Size() != expected {
		return fmt.Errorf("压缩包内文件 %q 大小与声明不一致", file.Name)
	}
	return nil
}

func extractTarGz(path, dest string, limits archiveLimits) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	gzipReader, err := gzip.NewReader(file)
	if err != nil {
		return fmt.Errorf("无法打开 gzip：%w", err)
	}
	defer gzipReader.Close()
	reader := tar.NewReader(gzipReader)
	var total int64
	count := 0
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("无法读取 tar：%w", err)
		}
		rel, err := safeArchivePath(header.Name)
		if err != nil {
			return fmt.Errorf("压缩包包含不安全路径 %q：%w", header.Name, err)
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(filepath.Join(dest, rel), 0o755); err != nil {
				return err
			}
			continue
		case tar.TypeReg, tar.TypeRegA:
		case tar.TypeXGlobalHeader, tar.TypeXHeader, tar.TypeGNULongName, tar.TypeGNULongLink:
			continue
		default:
			return fmt.Errorf("压缩包包含不支持的文件类型 %q（type %d）", header.Name, header.Typeflag)
		}
		count++
		if limits.MaxFiles > 0 && count > limits.MaxFiles {
			return fmt.Errorf("压缩包文件数超过限制（%d）", limits.MaxFiles)
		}
		if header.Size < 0 {
			return fmt.Errorf("压缩包内文件 %q 大小无效", header.Name)
		}
		if limits.MaxFileBytes > 0 && header.Size > limits.MaxFileBytes {
			return fmt.Errorf("压缩包内文件 %q 超过单文件限制", header.Name)
		}
		total += header.Size
		if limits.MaxBytes > 0 && total > limits.MaxBytes {
			return fmt.Errorf("压缩包解压后体积超过限制（%d MiB）", limits.MaxBytes/1024/1024)
		}
		target := filepath.Join(dest, rel)
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		perm := fs.FileMode(header.Mode) & 0o777
		if perm == 0 {
			perm = 0o644
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, perm)
		if err != nil {
			return err
		}
		_, copyErr := copyLimited(out, reader, limits.MaxFileBytes)
		closeErr := out.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
}

func copyLimited(dst io.Writer, src io.Reader, limit int64) (int64, error) {
	if limit <= 0 {
		return io.Copy(dst, src)
	}
	written, err := io.Copy(dst, io.LimitReader(src, limit+1))
	if err != nil {
		return written, err
	}
	if written > limit {
		return written, errors.New("文件超过大小限制")
	}
	return written, nil
}

func safeArchivePath(name string) (string, error) {
	name = strings.ReplaceAll(name, "\\", "/")
	if name == "" || strings.ContainsRune(name, 0) {
		return "", errors.New("空路径或包含 NUL")
	}
	if strings.HasPrefix(name, "/") || strings.HasPrefix(name, "//") {
		return "", errors.New("绝对路径")
	}
	if len(name) >= 2 && name[1] == ':' {
		return "", errors.New("包含盘符")
	}
	parts := strings.Split(name, "/")
	cleaned := make([]string, 0, len(parts))
	for _, part := range parts {
		if part == "" || part == "." {
			continue
		}
		if part == ".." {
			return "", errors.New("包含 ..")
		}
		if err := validateWindowsName(part); err != nil {
			return "", err
		}
		cleaned = append(cleaned, part)
	}
	if len(cleaned) == 0 {
		return "", errors.New("路径为空")
	}
	rel := filepath.FromSlash(strings.Join(cleaned, "/"))
	if filepath.IsAbs(rel) {
		return "", errors.New("绝对路径")
	}
	return rel, nil
}

func validateWindowsName(name string) error {
	if name == "" {
		return errors.New("空文件名")
	}
	if strings.HasSuffix(name, ".") || strings.HasSuffix(name, " ") {
		return errors.New("文件名不能以点或空格结尾")
	}
	for _, char := range name {
		if char < 32 {
			return errors.New("文件名包含控制字符")
		}
		switch char {
		case ':', '<', '>', '"', '|', '?', '*':
			return fmt.Errorf("文件名包含非法字符 %q", char)
		}
	}
	base := name
	if index := strings.IndexByte(base, '.'); index >= 0 {
		base = base[:index]
	}
	switch strings.ToUpper(base) {
	case "CON", "PRN", "AUX", "NUL", "COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9",
		"LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9":
		return fmt.Errorf("文件名 %q 是 Windows 保留名", name)
	}
	if len(name) > 200 {
		return errors.New("文件名过长")
	}
	return nil
}
