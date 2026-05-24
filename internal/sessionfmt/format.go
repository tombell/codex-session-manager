package sessionfmt

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/tombell/codex-session-manager/internal/sessions"
)

func DisplayTitle(session sessions.Session) string {
	if session.Title != "" {
		return session.Title
	}
	if session.FirstPrompt != "" {
		return session.FirstPrompt
	}
	return filepath.Base(session.Path)
}

func ShortPath(path string) string {
	home, err := os.UserHomeDir()
	if err == nil && strings.HasPrefix(path, home) {
		path = "~" + strings.TrimPrefix(path, home)
	}
	if len(path) <= 58 {
		return path
	}
	return "..." + path[len(path)-55:]
}

func HumanSize(size int64) string {
	const unit = 1024
	if size < unit {
		return fmt.Sprintf("%d B", size)
	}
	div, exp := int64(unit), 0
	for n := size / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(size)/float64(div), "KMGTPE"[exp])
}
