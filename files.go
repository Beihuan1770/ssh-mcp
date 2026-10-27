package main

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	pathpkg "path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/pkg/sftp"
)

func ensureRemoteDir(sc *sftp.Client, remotePath string) error {
	cleaned := pathpkg.Clean(remotePath)
	if cleaned == "" || cleaned == "." || cleaned == "/" {
		return nil
	}
	current := "/"
	if !strings.HasPrefix(cleaned, "/") {
		current = ""
	}
	for _, part := range strings.Split(strings.TrimPrefix(cleaned, "/"), "/") {
		if part == "" {
			continue
		}
		current = pathpkg.Join(current, part)
		info, err := sc.Stat(current)
		if err == nil {
			if !info.IsDir() {
				return fmt.Errorf("路径已存在但不是目录: %s", current)
			}
			continue
		}
		if err := sc.Mkdir(current); err != nil {
			if info2, err2 := sc.Stat(current); err2 == nil && info2.IsDir() {
				continue
			}
			return fmt.Errorf("创建目录 %s 失败: %w", current, err)
		}
	}
	return nil
}

func uniqueRemotePath(sc *sftp.Client, base string) string {
	candidate := base
	for index := 1; index < 1000; index++ {
		if _, err := sc.Stat(candidate); err != nil {
			return candidate
		}
		candidate = fmt.Sprintf("%s.%d", base, index)
	}
	return fmt.Sprintf("%s.%d", base, time.Now().UnixNano())
}

func readRemoteFile(connection *Connection, remotePath, encoding string, startLine, maxLines, maxBytes int) (string, error) {
	if maxBytes < 1024 {
		maxBytes = 1024
	}
	if maxBytes > 8*1024*1024 {
		maxBytes = 8 * 1024 * 1024
	}
	if maxLines <= 0 {
		maxLines = 2000
	}
	if startLine <= 0 {
		startLine = 1
	}

	var output string
	err := connection.WithSFTP(func(sc *sftp.Client) error {
		info, err := sc.Stat(remotePath)
		if err != nil {
			return fmt.Errorf("读取失败: %w", err)
		}
		if info.IsDir() {
			return fmt.Errorf("路径是目录，请用 ssh_list: %s", remotePath)
		}
		file, err := sc.Open(remotePath)
		if err != nil {
			return fmt.Errorf("读取失败: %w", err)
		}
		defer file.Close()

		head := make([]byte, 4096)
		n, _ := io.ReadFull(file, head)
		head = head[:n]
		if bytes.IndexByte(head, 0) >= 0 {
			if _, err := file.Seek(0, io.SeekStart); err != nil {
				return fmt.Errorf("读取失败: %w", err)
			}
			data, _ := io.ReadAll(io.LimitReader(file, int64(maxBytes)+1))
			truncated := false
			if len(data) > maxBytes {
				data = data[:maxBytes]
				truncated = true
			}
			text := fmt.Sprintf("# %s 二进制文件 (size=%s, 前 %d 字节 base64)\n%s",
				remotePath, humanSize(info.Size()), len(data), base64.StdEncoding.EncodeToString(data))
			if truncated {
				text += "\n[截断] 超过 max_bytes"
			}
			output = text
			return nil
		}

		if _, err := file.Seek(0, io.SeekStart); err != nil {
			return fmt.Errorf("读取失败: %w", err)
		}
		reader := bufio.NewReaderSize(io.LimitReader(file, int64(maxBytes)+1), 64*1024)
		lineNo := 0
		collectedLines := 0
		bytesUsed := 0
		truncated := false
		var collected bytes.Buffer

		for {
			line, readErr := reader.ReadString('\n')
			if len(line) > 0 {
				lineNo++
				if lineNo >= startLine {
					if lineNo-startLine >= maxLines {
						truncated = true
						break
					}
					remaining := maxBytes - bytesUsed
					if remaining <= 0 {
						truncated = true
						break
					}
					if len(line) > remaining {
						collected.WriteString(line[:remaining])
						bytesUsed += remaining
						collectedLines++
						truncated = true
						break
					}
					collected.WriteString(line)
					bytesUsed += len(line)
					collectedLines++
				}
			}
			if readErr != nil {
				if errors.Is(readErr, io.EOF) {
					break
				}
				return fmt.Errorf("读取失败: %w", readErr)
			}
			if bytesUsed >= maxBytes {
				truncated = true
				break
			}
		}

		header := fmt.Sprintf("# %s (size=%s, encoding=%s)", remotePath, humanSize(info.Size()), encoding)
		if collected.Len() == 0 {
			body := "(空文件或起始行超出文件末尾)"
			if lineNo > 0 && startLine > lineNo {
				body = fmt.Sprintf("(文件只读到第 %d 行，起始行 %d 超出范围)", lineNo, startLine)
			}
			output = header + "\n" + body
			return nil
		}
		text, issue := decodeText(collected.Bytes(), encoding)
		endLine := startLine + collectedLines - 1
		header += fmt.Sprintf(" 行 %d-%d", startLine, endLine)
		output = header + "\n" + text
		if issue {
			output += "\n[编码] 内容含无法解码的字节，已用替换字符显示"
		}
		if truncated {
			output += fmt.Sprintf("\n[截断] 已返回 %d 字节；文件共 %s", bytesUsed, humanSize(info.Size()))
		}
		return nil
	})
	return output, err
}

func writeRemoteFile(connection *Connection, remotePath string, data []byte, mode string, backup, atomic, mkdirParents bool) (string, error) {
	var message string
	err := connection.WithSFTP(func(sc *sftp.Client) error {
		parent := pathpkg.Dir(remotePath)
		if mkdirParents {
			if err := ensureRemoteDir(sc, parent); err != nil {
				return err
			}
		} else if parent != "." && parent != "/" {
			info, err := sc.Stat(parent)
			if err != nil {
				return fmt.Errorf("父目录不存在: %s", parent)
			}
			if !info.IsDir() {
				return fmt.Errorf("父路径不是目录: %s", parent)
			}
		}

		existing, statErr := sc.Stat(remotePath)
		exists := statErr == nil
		if exists && existing.IsDir() {
			return fmt.Errorf("路径是目录，不能写入文件: %s", remotePath)
		}

		if mode == "append" {
			file, err := sc.OpenFile(remotePath, os.O_WRONLY|os.O_CREATE|os.O_APPEND)
			if err != nil {
				return fmt.Errorf("追加失败: %w", err)
			}
			_, writeErr := file.Write(data)
			closeErr := file.Close()
			if writeErr != nil {
				return fmt.Errorf("追加失败: %w", writeErr)
			}
			if closeErr != nil {
				return fmt.Errorf("追加失败: %w", closeErr)
			}
			message = fmt.Sprintf("追加成功: %s (%d 字节)", remotePath, len(data))
			return nil
		}

		if mode == "create" && exists {
			return fmt.Errorf("文件已存在，create 模式不覆盖: %s", remotePath)
		}

		backupPath := ""
		if backup && exists && mode == "overwrite" {
			stamp := time.Now().Format("20060102-150405")
			backupPath = uniqueRemotePath(sc, remotePath+".bak-"+stamp)
			if err := sc.Rename(remotePath, backupPath); err != nil {
				return fmt.Errorf("创建备份失败: %w", err)
			}
		}
		restoreBackup := func() {
			if backupPath != "" {
				_ = sc.Rename(backupPath, remotePath)
			}
		}

		if atomic {
			tempPath := uniqueRemotePath(sc, fmt.Sprintf("%s.tmp-%d-%d", remotePath, os.Getpid(), time.Now().UnixNano()))
			file, err := sc.Create(tempPath)
			if err != nil {
				restoreBackup()
				return fmt.Errorf("写入临时文件失败: %w", err)
			}
			_, writeErr := file.Write(data)
			closeErr := file.Close()
			if writeErr != nil || closeErr != nil {
				_ = sc.Remove(tempPath)
				restoreBackup()
				if writeErr != nil {
					return fmt.Errorf("写入临时文件失败: %w", writeErr)
				}
				return fmt.Errorf("关闭临时文件失败: %w", closeErr)
			}
			if exists {
				_ = sc.Chmod(tempPath, existing.Mode().Perm())
			}
			if err := sc.PosixRename(tempPath, remotePath); err != nil {
				if exists {
					_ = sc.Remove(remotePath)
				}
				if err2 := sc.Rename(tempPath, remotePath); err2 != nil {
					_ = sc.Remove(tempPath)
					restoreBackup()
					return fmt.Errorf("替换文件失败: %w", err2)
				}
			}
		} else {
			file, err := sc.Create(remotePath)
			if err != nil {
				restoreBackup()
				return fmt.Errorf("写入失败: %w", err)
			}
			_, writeErr := file.Write(data)
			closeErr := file.Close()
			if writeErr != nil || closeErr != nil {
				restoreBackup()
				if writeErr != nil {
					return fmt.Errorf("写入失败: %w", writeErr)
				}
				return fmt.Errorf("关闭失败: %w", closeErr)
			}
		}

		detail := fmt.Sprintf("%s, %d 字节", mode, len(data))
		if atomic {
			detail += ", 原子写"
		}
		if backupPath != "" {
			detail += ", 备份=" + backupPath
		}
		message = fmt.Sprintf("写入成功: %s (%s)", remotePath, detail)
		return nil
	})
	return message, err
}

func listRemote(connection *Connection, remotePath string, recursive bool, maxEntries int, showHidden bool) (string, error) {
	if maxEntries <= 0 {
		maxEntries = 500
	}
	var output string
	err := connection.WithSFTP(func(sc *sftp.Client) error {
		info, err := sc.Stat(remotePath)
		if err != nil {
			return fmt.Errorf("列目录失败: %w", err)
		}
		if !info.IsDir() {
			return fmt.Errorf("路径不是目录，请用 ssh_stat: %s", remotePath)
		}
		lines := []string{}
		truncated, collectErr := collectRemoteEntries(sc, remotePath, "", recursive, maxEntries, showHidden, &lines, 0)
		if collectErr != nil {
			return collectErr
		}
		header := fmt.Sprintf("目录: %s  条目: %d", remotePath, len(lines))
		if truncated {
			header += fmt.Sprintf("（已达到 max_entries=%d，结果被截断）", maxEntries)
		}
		if len(lines) == 0 {
			output = header + "\n(空目录)"
		} else {
			output = header + "\n" + strings.Join(lines, "\n")
		}
		return nil
	})
	return output, err
}

func collectRemoteEntries(sc *sftp.Client, dir, prefix string, recursive bool, maxEntries int, showHidden bool, lines *[]string, depth int) (bool, error) {
	entries, err := sc.ReadDir(dir)
	if err != nil {
		return false, fmt.Errorf("列目录失败: %w", err)
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].IsDir() != entries[j].IsDir() {
			return entries[i].IsDir()
		}
		return strings.ToLower(entries[i].Name()) < strings.ToLower(entries[j].Name())
	})
	for _, info := range entries {
		if !showHidden && strings.HasPrefix(info.Name(), ".") {
			continue
		}
		if len(*lines) >= maxEntries {
			return true, nil
		}
		*lines = append(*lines, formatRemoteInfo(prefix+info.Name(), info))
		if recursive && info.IsDir() && info.Mode()&os.ModeSymlink == 0 && depth < 20 {
			stop, err := collectRemoteEntries(sc, pathpkg.Join(dir, info.Name()), prefix+info.Name()+"/", recursive, maxEntries, showHidden, lines, depth+1)
			if err != nil {
				return false, err
			}
			if stop {
				return true, nil
			}
		}
	}
	return false, nil
}

func formatRemoteInfo(name string, info os.FileInfo) string {
	size := "-"
	if !info.IsDir() {
		size = humanSize(info.Size())
	}
	suffix := ""
	if info.IsDir() {
		suffix = "/"
	}
	return fmt.Sprintf("%s %8s %s  %s%s", info.Mode().String(), size, info.ModTime().Format("2006-01-02 15:04:05"), name, suffix)
}

func statRemote(connection *Connection, remotePath string) (string, error) {
	var output string
	err := connection.WithSFTP(func(sc *sftp.Client) error {
		info, err := sc.Lstat(remotePath)
		if err != nil {
			return fmt.Errorf("查看失败: %w", err)
		}
		kind := "其他"
		switch {
		case info.IsDir():
			kind = "目录"
		case info.Mode()&os.ModeSymlink != 0:
			kind = "符号链接"
		case info.Mode().IsRegular():
			kind = "文件"
		}
		lines := []string{
			"路径: " + remotePath,
			"类型: " + kind,
			fmt.Sprintf("大小: %d 字节 (%s)", info.Size(), humanSize(info.Size())),
			"权限: " + info.Mode().String(),
		}
		accessTime := info.ModTime()
		if raw, ok := info.Sys().(*sftp.FileStat); ok {
			lines = append(lines, fmt.Sprintf("属主: uid=%d gid=%d", raw.UID, raw.GID))
			accessTime = raw.AccessTime()
		}
		lines = append(lines,
			"修改时间: "+info.ModTime().Format("2006-01-02 15:04:05"),
			"访问时间: "+accessTime.Format("2006-01-02 15:04:05"),
		)
		if kind == "符号链接" {
			if target, err := sc.ReadLink(remotePath); err == nil {
				lines = append(lines, "链接目标: "+target)
			}
		}
		output = strings.Join(lines, "\n")
		return nil
	})
	return output, err
}

func mkdirRemote(connection *Connection, remotePath string, parents bool) (string, error) {
	var output string
	err := connection.WithSFTP(func(sc *sftp.Client) error {
		if info, err := sc.Stat(remotePath); err == nil {
			if info.IsDir() {
				output = "目录已存在: " + remotePath
				return nil
			}
			return fmt.Errorf("路径已存在且不是目录: %s", remotePath)
		}
		if parents {
			if err := sc.MkdirAll(remotePath); err != nil {
				return fmt.Errorf("创建目录失败: %w", err)
			}
		} else if err := sc.Mkdir(remotePath); err != nil {
			return fmt.Errorf("创建目录失败: %w", err)
		}
		output = "已创建目录: " + remotePath
		return nil
	})
	return output, err
}

func removeRemote(connection *Connection, remotePath string, recursive bool) (string, error) {
	cleaned := pathpkg.Clean(strings.TrimSpace(remotePath))
	if cleaned == "" || cleaned == "." || cleaned == ".." || cleaned == "/" || cleaned == "//" {
		return "", fmt.Errorf("拒绝删除受保护路径: %q", remotePath)
	}
	var output string
	err := connection.WithSFTP(func(sc *sftp.Client) error {
		info, err := sc.Lstat(cleaned)
		if err != nil {
			return fmt.Errorf("删除失败: %w", err)
		}
		if info.IsDir() && info.Mode()&os.ModeSymlink == 0 {
			if recursive {
				removed, err := removeTree(sc, cleaned)
				if err != nil {
					return err
				}
				output = fmt.Sprintf("已删除目录: %s（共 %d 项）", cleaned, removed)
				return nil
			}
			entries, err := sc.ReadDir(cleaned)
			if err != nil {
				return fmt.Errorf("列目录失败: %w", err)
			}
			if len(entries) > 0 {
				return fmt.Errorf("目录非空，需 recursive=true 才能删除: %s（%d 项）", cleaned, len(entries))
			}
			if err := sc.RemoveDirectory(cleaned); err != nil {
				return fmt.Errorf("删除目录失败: %w", err)
			}
			output = "已删除空目录: " + cleaned
			return nil
		}
		if err := sc.Remove(cleaned); err != nil {
			return fmt.Errorf("删除失败: %w", err)
		}
		output = "已删除: " + cleaned
		return nil
	})
	return output, err
}

func removeTree(sc *sftp.Client, remotePath string) (int, error) {
	entries, err := sc.ReadDir(remotePath)
	if err != nil {
		return 0, fmt.Errorf("列目录失败: %w", err)
	}
	removed := 0
	for _, info := range entries {
		child := pathpkg.Join(remotePath, info.Name())
		if info.IsDir() && info.Mode()&os.ModeSymlink == 0 {
			count, err := removeTree(sc, child)
			if err != nil {
				return removed, err
			}
			removed += count
		} else {
			if err := sc.Remove(child); err != nil {
				return removed, fmt.Errorf("删除失败: %w", err)
			}
			removed++
		}
	}
	if err := sc.RemoveDirectory(remotePath); err != nil {
		return removed, fmt.Errorf("删除目录失败: %w", err)
	}
	return removed + 1, nil
}

func moveRemote(connection *Connection, src, dst string, overwrite bool) (string, error) {
	var output string
	err := connection.WithSFTP(func(sc *sftp.Client) error {
		if _, err := sc.Lstat(src); err != nil {
			return fmt.Errorf("移动失败: %w", err)
		}
		destinationExists := false
		if _, err := sc.Lstat(dst); err == nil {
			destinationExists = true
		}
		if destinationExists && !overwrite {
			return fmt.Errorf("目标已存在（overwrite=false）: %s", dst)
		}
		if err := sc.PosixRename(src, dst); err != nil {
			if destinationExists && overwrite {
				_ = sc.Remove(dst)
			}
			if err2 := sc.Rename(src, dst); err2 != nil {
				return fmt.Errorf("移动失败: %w", err2)
			}
		}
		output = fmt.Sprintf("已移动: %s -> %s", src, dst)
		return nil
	})
	return output, err
}

func uploadPath(connection *Connection, localPath, remotePath string, recursive, overwrite bool) (string, error) {
	localPath = expandHome(localPath)
	info, err := os.Stat(localPath)
	if err != nil {
		return "", fmt.Errorf("本地路径不存在: %w", err)
	}
	if info.IsDir() && !recursive {
		return "", fmt.Errorf("local_path 是目录，上传目录需要 recursive=true")
	}

	count := 0
	total := int64(0)
	err = connection.WithSFTP(func(sc *sftp.Client) error {
		uploadFile := func(source, target string) error {
			if !overwrite {
				if _, err := sc.Stat(target); err == nil {
					return fmt.Errorf("远端文件已存在（overwrite=false）: %s", target)
				}
			}
			src, err := os.Open(source)
			if err != nil {
				return fmt.Errorf("打开本地文件失败: %w", err)
			}
			defer src.Close()
			dst, err := sc.Create(target)
			if err != nil {
				return fmt.Errorf("创建远端文件失败: %w", err)
			}
			written, copyErr := io.Copy(dst, src)
			closeErr := dst.Close()
			if copyErr != nil {
				return fmt.Errorf("上传失败: %w", copyErr)
			}
			if closeErr != nil {
				return fmt.Errorf("关闭远端文件失败: %w", closeErr)
			}
			count++
			total += written
			return nil
		}

		if info.IsDir() {
			base := localPath
			return filepath.Walk(base, func(path string, walkInfo os.FileInfo, walkErr error) error {
				if walkErr != nil {
					return walkErr
				}
				relative, err := filepath.Rel(base, path)
				if err != nil {
					return err
				}
				target := remotePath
				if relative != "." {
					target = pathpkg.Join(remotePath, filepath.ToSlash(relative))
				}
				if walkInfo.IsDir() {
					return ensureRemoteDir(sc, target)
				}
				return uploadFile(path, target)
			})
		}

		parent := pathpkg.Dir(remotePath)
		if err := ensureRemoteDir(sc, parent); err != nil {
			return err
		}
		return uploadFile(localPath, remotePath)
	})
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("上传完成: %d 个文件, %s -> %s", count, humanSize(total), remotePath), nil
}

func downloadPath(connection *Connection, remotePath, localPath string, recursive, overwrite bool) (string, error) {
	localPath = expandHome(localPath)
	count := 0
	total := int64(0)
	skipped := 0

	err := connection.WithSFTP(func(sc *sftp.Client) error {
		downloadFile := func(remote, local string) error {
			if _, err := os.Stat(local); err == nil && !overwrite {
				return fmt.Errorf("本地文件已存在（overwrite=false）: %s", local)
			}
			if err := os.MkdirAll(filepath.Dir(local), 0o755); err != nil {
				return fmt.Errorf("创建本地目录失败: %w", err)
			}
			dst, err := os.Create(local)
			if err != nil {
				return fmt.Errorf("创建本地文件失败: %w", err)
			}
			src, err := sc.Open(remote)
			if err != nil {
				dst.Close()
				return fmt.Errorf("打开远端文件失败: %w", err)
			}
			written, copyErr := io.Copy(dst, src)
			closeSrcErr := src.Close()
			closeDstErr := dst.Close()
			if copyErr != nil {
				return fmt.Errorf("下载失败: %w", copyErr)
			}
			if closeSrcErr != nil {
				return fmt.Errorf("关闭远端文件失败: %w", closeSrcErr)
			}
			if closeDstErr != nil {
				return fmt.Errorf("关闭本地文件失败: %w", closeDstErr)
			}
			count++
			total += written
			return nil
		}

		var downloadTree func(remoteDir, localDir string) error
		downloadTree = func(remoteDir, localDir string) error {
			if err := os.MkdirAll(localDir, 0o755); err != nil {
				return fmt.Errorf("创建本地目录失败: %w", err)
			}
			entries, err := sc.ReadDir(remoteDir)
			if err != nil {
				return fmt.Errorf("列目录失败: %w", err)
			}
			for _, info := range entries {
				remoteChild := pathpkg.Join(remoteDir, info.Name())
				localChild := filepath.Join(localDir, info.Name())
				if info.IsDir() && info.Mode()&os.ModeSymlink == 0 {
					if err := downloadTree(remoteChild, localChild); err != nil {
						return err
					}
				} else if info.Mode()&os.ModeSymlink != 0 {
					skipped++
				} else {
					if err := downloadFile(remoteChild, localChild); err != nil {
						return err
					}
				}
			}
			return nil
		}

		info, err := sc.Lstat(remotePath)
		if err != nil {
			return fmt.Errorf("下载失败: %w", err)
		}
		if info.IsDir() {
			if !recursive {
				return fmt.Errorf("remote_path 是目录，下载目录需要 recursive=true")
			}
			if localInfo, err := os.Stat(localPath); err == nil && !localInfo.IsDir() {
				return fmt.Errorf("本地路径已存在且不是目录: %s", localPath)
			}
			return downloadTree(remotePath, localPath)
		}
		return downloadFile(remotePath, localPath)
	})
	if err != nil {
		return "", err
	}
	message := fmt.Sprintf("下载完成: %d 个文件, %s -> %s", count, humanSize(total), localPath)
	if skipped > 0 {
		message += fmt.Sprintf("（跳过 %d 个符号链接）", skipped)
	}
	return message, nil
}
