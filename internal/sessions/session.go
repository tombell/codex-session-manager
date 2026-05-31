package sessions

import (
	"bufio"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	_ "modernc.org/sqlite"
)

type Session struct {
	ID          string
	Title       string
	Timestamp   time.Time
	CWD         string
	Path        string
	Relative    string
	Size        int64
	FirstPrompt string
	Malformed   bool
}

type Options struct {
	SessionsDir string
	BackupDir   string
	StateDB     string
	DryRun      bool
}

func DefaultSessionsDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".codex", "sessions")
}

func DefaultBackupBaseDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".codex", "session-backups")
}

func DefaultStateDBPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	newPath := filepath.Join(home, ".codex", "sqlite", "state_5.sqlite")
	if _, err := os.Stat(newPath); err == nil {
		return newPath
	}
	return filepath.Join(home, ".codex", "state_5.sqlite")
}

func Scan(root string) ([]Session, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}

	var found []Session
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() || filepath.Ext(path) != ".jsonl" {
			return nil
		}
		session, err := ParseFile(root, path)
		if err != nil {
			return err
		}
		found = append(found, session)
		return nil
	})
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []Session{}, nil
		}
		return nil, err
	}

	sort.SliceStable(found, func(i, j int) bool {
		left, right := found[i], found[j]
		if !left.Timestamp.Equal(right.Timestamp) {
			return left.Timestamp.After(right.Timestamp)
		}
		return left.Path > right.Path
	})
	return found, nil
}

func ScanWithTitles(root, stateDB string) ([]Session, error) {
	found, err := Scan(root)
	if err != nil {
		return nil, err
	}
	if stateDB == "" {
		return found, nil
	}
	metadata, err := LoadMetadata(stateDB)
	if err != nil {
		return found, nil
	}
	for idx := range found {
		meta := metadata[found[idx].Path]
		if meta.Title != "" {
			found[idx].Title = meta.Title
		}
		if meta.FirstPrompt != "" {
			found[idx].FirstPrompt = meta.FirstPrompt
		}
		if meta.CWD != "" {
			found[idx].CWD = meta.CWD
		}
	}
	return found, nil
}

type Metadata struct {
	Title       string
	FirstPrompt string
	CWD         string
}

func LoadTitles(stateDB string) (map[string]string, error) {
	metadata, err := LoadMetadata(stateDB)
	if err != nil {
		return nil, err
	}
	titles := map[string]string{}
	for path, meta := range metadata {
		if meta.Title != "" {
			titles[path] = meta.Title
		}
	}
	return titles, nil
}

func LoadMetadata(stateDB string) (map[string]Metadata, error) {
	if _, err := os.Stat(stateDB); err != nil {
		return nil, err
	}

	db, err := sql.Open("sqlite", stateDB)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	rows, err := db.Query("select rollout_path, title, first_user_message, cwd from threads where rollout_path != ''")
	if err == nil {
		defer rows.Close()
		return scanMetadataRows(rows)
	}

	rows, err = db.Query("select rollout_path, title from threads where title != '' and rollout_path != ''")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	metadata := map[string]Metadata{}
	for rows.Next() {
		var rolloutPath, title string
		if err := rows.Scan(&rolloutPath, &title); err != nil {
			return nil, err
		}
		metadata[rolloutPath] = Metadata{Title: titleText(title)}
	}
	return metadata, rows.Err()
}

func scanMetadataRows(rows *sql.Rows) (map[string]Metadata, error) {
	metadata := map[string]Metadata{}
	for rows.Next() {
		var rolloutPath, title, firstUserMessage, cwd string
		if err := rows.Scan(&rolloutPath, &title, &firstUserMessage, &cwd); err != nil {
			return nil, err
		}
		metadata[rolloutPath] = Metadata{Title: titleText(title), FirstPrompt: promptText(firstUserMessage), CWD: cwd}
	}
	return metadata, rows.Err()
}

func ParseFile(root, path string) (Session, error) {
	info, err := os.Stat(path)
	if err != nil {
		return Session{}, err
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return Session{}, err
	}
	session := Session{
		Path:     path,
		Relative: rel,
		Size:     info.Size(),
	}

	file, err := os.Open(path)
	if err != nil {
		return Session{}, err
	}
	defer file.Close()

	if err := parseJSONL(file, &session); err != nil {
		session.Malformed = true
	}
	if session.Timestamp.IsZero() {
		session.Timestamp = info.ModTime()
	}
	return session, nil
}

func Backup(root, backupBase string, selected []Session, now time.Time, dryRun bool) (string, error) {
	if len(selected) == 0 {
		return "", nil
	}
	if backupBase == "" {
		backupBase = DefaultBackupBaseDir()
	}
	target := filepath.Join(backupBase, now.Format("20060102-150405"))
	for _, session := range selected {
		if err := validateSessionPath(root, session.Path); err != nil {
			return "", err
		}
		rel, err := filepath.Rel(root, session.Path)
		if err != nil {
			return "", err
		}
		if dryRun {
			continue
		}
		dst := filepath.Join(target, rel)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return "", err
		}
		if err := copyFile(session.Path, dst); err != nil {
			return "", err
		}
	}
	return target, nil
}

func Delete(root string, selected []Session, dryRun bool) error {
	for _, session := range selected {
		if err := validateSessionPath(root, session.Path); err != nil {
			return err
		}
		if filepath.Ext(session.Path) != ".jsonl" {
			return fmt.Errorf("refusing to delete non-jsonl path: %s", session.Path)
		}
		if !dryRun {
			if err := os.Remove(session.Path); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
	}
	if dryRun {
		return nil
	}
	return pruneEmptyDirs(root)
}

func parseJSONL(r io.Reader, session *Session) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	var parseErr error
	for scanner.Scan() {
		var record map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			parseErr = err
			continue
		}
		recordType, _ := record["type"].(string)
		payload, _ := record["payload"].(map[string]any)
		if recordType == "session_meta" {
			readMeta(payload, session)
		}
		if session.FirstPrompt == "" {
			session.FirstPrompt = firstPrompt(recordType, payload)
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return parseErr
}

func readMeta(payload map[string]any, session *Session) {
	if id, ok := payload["id"].(string); ok {
		session.ID = id
	}
	if cwd, ok := payload["cwd"].(string); ok {
		session.CWD = cwd
	}
	if raw, ok := payload["timestamp"].(string); ok {
		if ts, err := time.Parse(time.RFC3339Nano, raw); err == nil {
			session.Timestamp = ts
		}
	}
}

func firstPrompt(recordType string, payload map[string]any) string {
	if recordType == "event_msg" {
		if msgType, _ := payload["type"].(string); msgType == "user_message" {
			if message, ok := payload["message"].(string); ok {
				return promptText(message)
			}
		}
	}
	if recordType != "response_item" {
		return ""
	}
	if role, _ := payload["role"].(string); role != "user" {
		return ""
	}
	content, ok := payload["content"].([]any)
	if !ok {
		return ""
	}
	for _, entry := range content {
		obj, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		if typ, _ := obj["type"].(string); typ != "input_text" {
			continue
		}
		if text, ok := obj["text"].(string); ok {
			if prompt := promptText(text); prompt != "" {
				return prompt
			}
		}
	}
	return ""
}

func validateSessionPath(root, path string) error {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	pathAbs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(rootAbs, pathAbs)
	if err != nil {
		return err
	}
	if rel == "." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) || rel == ".." || filepath.IsAbs(rel) {
		return fmt.Errorf("refusing path outside sessions root: %s", path)
	}
	return nil
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
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, info.Mode().Perm())
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

func pruneEmptyDirs(root string) error {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	var dirs []string
	if err := filepath.WalkDir(rootAbs, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() && path != rootAbs {
			dirs = append(dirs, path)
		}
		return nil
	}); err != nil {
		return err
	}
	sort.Slice(dirs, func(i, j int) bool { return len(dirs[i]) > len(dirs[j]) })
	for _, dir := range dirs {
		if err := os.Remove(dir); err != nil && !errors.Is(err, os.ErrNotExist) && !errors.Is(err, syscall.ENOTEMPTY) {
			return err
		}
	}
	return nil
}

func titleText(text string) string {
	text = strings.TrimSpace(text)
	if text == "" || isScaffolding(text) {
		return ""
	}
	return compact(text)
}

func promptText(text string) string {
	text = strings.TrimSpace(text)
	if text == "" || isScaffolding(text) {
		return ""
	}
	if idx := strings.Index(text, "] user: "); strings.HasPrefix(text, "[") && idx > 0 {
		text = text[idx+len("] user: "):]
	}
	return compact(text)
}

func isScaffolding(text string) bool {
	return strings.HasPrefix(text, "<") ||
		strings.HasPrefix(text, "# AGENTS.md") ||
		strings.HasPrefix(text, ">>> TRANSCRIPT") ||
		strings.HasPrefix(text, "The following is the Codex agent history")
}

func compact(text string) string {
	return strings.Join(strings.Fields(text), " ")
}
