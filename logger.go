package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	maxLogSize = 3 * 1024 * 1024 // 3MB
	maxBackups = 3               // 保留 3 份历史轮转文件
)

var (
	logMu          sync.Mutex
	logFile        *os.File
	logFilePathVal string
	currentLogSize int64
	logOutput      io.Writer

	logSubMu    sync.Mutex
	logSubs     = map[chan LogChunk]struct{}{}
	logSequence uint64
)

type LogChunk struct {
	Sequence uint64
	Text     string
	Reset    bool
}

func GetLogFilePath() string {
	return logFilePathVal
}

func InitLogger() {
	_ = os.MkdirAll(globalPkgVar, 0755)
	logFilePathVal = filepath.Join(globalPkgVar, "openchamber.log")

	logMu.Lock()
	defer logMu.Unlock()

	var err error
	logFile, err = os.OpenFile(logFilePathVal, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err == nil {
		if fi, err := logFile.Stat(); err == nil {
			currentLogSize = fi.Size()
		}
		logOutput = io.MultiWriter(logFile, broadcastWriter{})
	} else {
		logOutput = io.MultiWriter(os.Stdout, broadcastWriter{})
	}
}

// cleanOldBackups 清理超出数量的最旧历史归档，保持最新 maxBackups 份
func cleanOldBackups(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	var backups []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if (strings.HasPrefix(name, "openchamber-") && strings.HasSuffix(name, ".log")) || strings.HasPrefix(name, "openchamber.log.") {
			backups = append(backups, name)
		}
	}
	sort.Strings(backups)
	for len(backups) > maxBackups {
		_ = os.Remove(filepath.Join(dir, backups[0]))
		backups = backups[1:]
	}
}

// rotateLocked 在持有 logMu 时执行日志轮转（以创建日期时间命名历史归档）
func rotateLocked() {
	if logFile != nil {
		_ = logFile.Close()
		logFile = nil
	}

	dir := filepath.Dir(logFilePathVal)
	nowStr := time.Now().Format("2006-01-02_15-04-05")
	backupPath := filepath.Join(dir, fmt.Sprintf("openchamber-%s.log", nowStr))
	if _, err := os.Stat(backupPath); err == nil {
		backupPath = filepath.Join(dir, fmt.Sprintf("openchamber-%s_%d.log", nowStr, time.Now().Nanosecond()/1000))
	}

	if _, err := os.Stat(logFilePathVal); err == nil {
		_ = os.Rename(logFilePathVal, backupPath)
	}

	cleanOldBackups(dir)

	var err error
	logFile, err = os.OpenFile(logFilePathVal, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err == nil {
		currentLogSize = 0
		logOutput = io.MultiWriter(logFile, broadcastWriter{})
		logSequence++
		broadcastLog(LogChunk{Sequence: logSequence, Reset: true})
	} else {
		logOutput = io.MultiWriter(os.Stdout, broadcastWriter{})
	}
}

// ClearLogs 仅清空当前活跃日志文件
func ClearLogs() error {
	logMu.Lock()
	defer logMu.Unlock()

	if logFile != nil {
		_ = logFile.Close()
		logFile = nil
	}

	var err error
	logFile, err = os.OpenFile(logFilePathVal, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err == nil {
		currentLogSize = 0
		logOutput = io.MultiWriter(logFile, broadcastWriter{})
		logSequence++
		broadcastLog(LogChunk{Sequence: logSequence, Reset: true})
		return nil
	}
	logOutput = io.MultiWriter(os.Stdout, broadcastWriter{})
	return err
}

type broadcastWriter struct{}

func (broadcastWriter) Write(p []byte) (int, error) {
	logSequence++
	broadcastLog(LogChunk{Sequence: logSequence, Text: string(p)})
	return len(p), nil
}

func broadcastLog(chunk LogChunk) {
	logSubMu.Lock()
	defer logSubMu.Unlock()
	for ch := range logSubs {
		select {
		case ch <- chunk:
		default:
			select {
			case <-ch:
			default:
			}
			select {
			case ch <- LogChunk{Sequence: chunk.Sequence, Reset: true}:
			default:
			}
		}
	}
}

// SubscribeLog 订阅日志增量，返回的函数用于取消订阅
func SubscribeLog(buf int) (<-chan LogChunk, func()) {
	ch := make(chan LogChunk, buf)
	logSubMu.Lock()
	logSubs[ch] = struct{}{}
	logSubMu.Unlock()
	return ch, func() {
		logSubMu.Lock()
		delete(logSubs, ch)
		logSubMu.Unlock()
	}
}

func readLogPayload(limit int) (LogPayload, error) {
	logMu.Lock()
	defer logMu.Unlock()
	lines, content, err := readLastNLines(GetLogFilePath(), limit)
	if os.IsNotExist(err) {
		lines, content, err = []string{}, "", nil
	}
	return LogPayload{Lines: lines, Content: content, Sequence: logSequence}, err
}

func writeLogEntry(level, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	timestamp := time.Now().Format("2006-01-02 15:04:05")
	entry := fmt.Sprintf("%s [%s] %s\n", timestamp, level, msg)
	writeLogOutput(entry)
}

var logLevelPrefixRe = regexp.MustCompile(`^\[\s*(?:INFO|WARN|WARNING|ERROR|FATAL)\s*\]\s*`)

func writeRawLogLine(msg string) {
	level := "None"
	content := msg
	if match := logLevelPrefixRe.FindString(msg); match != "" {
		level = strings.Trim(strings.TrimSpace(match), "[]")
		content = strings.TrimSpace(strings.TrimPrefix(msg, match))
	}
	timestamp := time.Now().Format("2006-01-02 15:04:05")
	entry := fmt.Sprintf("%s [%s] %s\n", timestamp, level, content)
	writeLogOutput(entry)
}

func writeLogOutput(entry string) {
	entryLen := int64(len(entry))

	logMu.Lock()
	defer logMu.Unlock()

	if logFile != nil && currentLogSize+entryLen >= maxLogSize {
		rotateLocked()
	}

	if logOutput != nil {
		_, _ = io.WriteString(logOutput, entry)
		currentLogSize += entryLen
	} else {
		_, _ = os.Stdout.WriteString(entry)
	}
}

func LogInfo(format string, args ...any) {
	writeLogEntry("INFO", format, args...)
}

func LogWarning(format string, args ...any) {
	writeLogEntry("WARN", format, args...)
}

func LogError(format string, args ...any) {
	writeLogEntry("ERROR", format, args...)
}

func LogFatal(format string, args ...any) {
	writeLogEntry("FATAL", format, args...)
	os.Exit(1)
}

var ansiRegex = regexp.MustCompile(`\x1b\[[0-9;?]*[a-zA-Z]|\x1b\([a-zA-Z]`)

func cleanLogLine(b []byte) string {
	s := string(b)
	s = ansiRegex.ReplaceAllString(s, "")
	s = strings.TrimRight(s, "\r\n ")
	s = strings.TrimLeft(s, "\r\n")
	return s
}

// LineLogWriter 行缓冲写入器，保留外部子进程原生输出
type LineLogWriter struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

// NewLogWriter 创建保留外部输出格式的行缓冲写入器
func NewLogWriter() *LineLogWriter {
	return &LineLogWriter{}
}

func (w *LineLogWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n := len(p)
	w.buf.Write(p)
	for {
		b := w.buf.Bytes()
		idx := bytes.IndexByte(b, '\n')
		if idx < 0 {
			break
		}
		line := b[:idx]
		clean := cleanLogLine(line)
		if clean != "" {
			writeRawLogLine(clean)
		}
		w.buf.Next(idx + 1)
	}
	return n, nil
}

// Flush 刷出缓冲区残留内容
func (w *LineLogWriter) Flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.buf.Len() > 0 {
		clean := cleanLogLine(w.buf.Bytes())
		w.buf.Reset()
		if clean != "" {
			writeRawLogLine(clean)
		}
	}
}
