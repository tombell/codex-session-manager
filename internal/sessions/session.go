package sessions

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
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
	Subagent    bool
	ParentID    string
}

type Options struct {
	SessionsDir      string
	BackupDir        string
	StateDB          string
	DryRun           bool
	IncludeSubagents bool
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

// FilterSubagents returns only top-level (non-subagent) sessions. Codex spawns
// subagent threads (for example the "guardian" approval reviewer) that each get
// their own rollout file; these would otherwise appear as duplicate sessions.
func FilterSubagents(in []Session) []Session {
	out := in[:0:0]
	for _, session := range in {
		if session.Subagent {
			continue
		}
		out = append(out, session)
	}
	return out
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

	// Codex 0.145 added a user-facing thread name. Prefer it over the
	// generated title while retaining compatibility with older databases.
	queries := []string{
		"select rollout_path, coalesce(nullif(trim(name), ''), title), first_user_message, cwd from threads where rollout_path != ''",
		"select rollout_path, title, first_user_message, cwd from threads where rollout_path != ''",
	}
	for _, query := range queries {
		rows, queryErr := db.Query(query)
		if queryErr != nil {
			continue
		}
		metadata, scanErr := scanMetadataRows(rows)
		rows.Close()
		return metadata, scanErr
	}

	rows, err := db.Query("select rollout_path, title from threads where title != '' and rollout_path != ''")
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

var codexExecutable = "codex"

func Delete(root, stateDB string, selected []Session, dryRun bool) error {
	for _, session := range selected {
		if err := validateSessionPath(root, session.Path); err != nil {
			return err
		}
		if filepath.Ext(session.Path) != ".jsonl" {
			return fmt.Errorf("refusing to delete non-jsonl path: %s", session.Path)
		}
	}
	if dryRun {
		return nil
	}

	withID := make([]Session, 0, len(selected))
	withoutID := make([]Session, 0, len(selected))
	for _, session := range selected {
		if session.ID == "" && stateDB != "" {
			id, err := loadThreadID(stateDB, session.Path)
			if err != nil {
				return err
			}
			session.ID = id
		}
		if session.ID == "" {
			withoutID = append(withoutID, session)
		} else {
			withID = append(withID, session)
		}
	}

	if len(withID) > 0 {
		codexHome, err := codexHomeForSessions(root)
		if err != nil {
			return err
		}
		if err := deleteCodexThreads(codexHome, withID); err != nil {
			return err
		}
	}

	// A file without a thread id has no chat that the app-server can address.
	// Preserve the old cleanup behavior for such malformed rollout files.
	for _, session := range withoutID {
		if err := os.Remove(session.Path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return pruneEmptyDirs(root)
}

func loadThreadID(stateDB, rolloutPath string) (string, error) {
	if _, err := os.Stat(stateDB); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		return "", err
	}
	db, err := sql.Open("sqlite", stateDB)
	if err != nil {
		return "", err
	}
	defer db.Close()

	var id string
	err = db.QueryRow("select id from threads where rollout_path = ? limit 1", rolloutPath).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return id, err
}

func codexHomeForSessions(root string) (string, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	if filepath.Base(root) != "sessions" {
		return "", fmt.Errorf("cannot delete Codex chat: sessions root must end in %q: %s", "sessions", root)
	}
	return filepath.Dir(root), nil
}

func deleteCodexThreads(codexHome string, selected []Session) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(ctx, codexExecutable, "app-server", "--stdio")
	cmd.Env = envWith(os.Environ(), "CODEX_HOME", codexHome)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start Codex app-server: %w", err)
	}

	encoder := json.NewEncoder(stdin)
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	finish := func() error {
		_ = stdin.Close()
		return cmd.Wait()
	}
	fail := func(err error) error {
		_ = finish()
		if ctx.Err() != nil {
			return fmt.Errorf("Codex app-server timed out: %w", ctx.Err())
		}
		if detail := strings.TrimSpace(stderr.String()); detail != "" {
			return fmt.Errorf("%w: %s", err, compact(detail))
		}
		return err
	}

	initialize := map[string]any{
		"method": "initialize",
		"id":     1,
		"params": map[string]any{"clientInfo": map[string]string{
			"name": "codex_session_manager", "title": "Codex Session Manager", "version": "dev",
		}},
	}
	if err := encoder.Encode(initialize); err != nil {
		return fail(err)
	}
	if err := readCodexResponse(scanner, 1); err != nil {
		return fail(fmt.Errorf("initialize Codex app-server: %w", err))
	}
	if err := encoder.Encode(map[string]any{"method": "initialized"}); err != nil {
		return fail(err)
	}

	for idx, session := range selected {
		requestID := idx + 2
		request := map[string]any{
			"method": "thread/delete",
			"id":     requestID,
			"params": map[string]string{"threadId": session.ID},
		}
		if err := encoder.Encode(request); err != nil {
			return fail(err)
		}
		if err := readCodexResponse(scanner, requestID); err != nil {
			// Deleting a parent also deletes its spawned descendants. A selected
			// descendant may therefore be gone by the time its request runs.
			if _, statErr := os.Stat(session.Path); errors.Is(statErr, os.ErrNotExist) {
				continue
			}
			return fail(fmt.Errorf("delete Codex chat %s: %w", session.ID, err))
		}
	}

	if err := finish(); err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("Codex app-server timed out: %w", ctx.Err())
		}
		return fmt.Errorf("Codex app-server exited: %w", err)
	}
	return nil
}

func readCodexResponse(scanner *bufio.Scanner, requestID int) error {
	for scanner.Scan() {
		var message struct {
			ID    json.RawMessage `json:"id"`
			Error *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &message); err != nil {
			return fmt.Errorf("invalid Codex app-server response: %w", err)
		}
		if len(message.ID) == 0 {
			continue
		}
		var gotID int
		if err := json.Unmarshal(message.ID, &gotID); err != nil || gotID != requestID {
			continue
		}
		if message.Error != nil {
			return errors.New(message.Error.Message)
		}
		return nil
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return errors.New("Codex app-server closed without a response")
}

func envWith(env []string, key, value string) []string {
	prefix := key + "="
	out := make([]string, 0, len(env)+1)
	for _, entry := range env {
		if !strings.HasPrefix(entry, prefix) {
			out = append(out, entry)
		}
	}
	return append(out, prefix+value)
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
	if parent, ok := payload["parent_thread_id"].(string); ok && parent != "" {
		session.ParentID = parent
		session.Subagent = true
	}
	if threadSource, ok := payload["thread_source"].(string); ok && threadSource == "subagent" {
		session.Subagent = true
	}
	if source, ok := payload["source"].(map[string]any); ok {
		if _, ok := source["subagent"]; ok {
			session.Subagent = true
		}
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
