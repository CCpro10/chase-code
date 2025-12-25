package persistence

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"chase-code/server/llm"
)

const sessionDirName = "sessions"

func getSessionDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, ".chase-code", sessionDirName)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", err
	}
	return dir, nil
}

type StoredSession struct {
	ID        string             `json:"id"`
	UpdatedAt time.Time          `json:"updated_at"`
	History   []llm.ResponseItem `json:"history"`
}

// Save 保存会话历史。
func Save(id string, history []llm.ResponseItem) error {
	dir, err := getSessionDir()
	if err != nil {
		return err
	}

	path := filepath.Join(dir, id+".json")
	data := StoredSession{
		ID:        id,
		UpdatedAt: time.Now(),
		History:   history,
	}

	bytes, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(path, bytes, 0644)
}

// Load 加载会话历史。
func Load(id string) ([]llm.ResponseItem, error) {
	dir, err := getSessionDir()
	if err != nil {
		return nil, err
	}

	path := filepath.Join(dir, id+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var sess StoredSession
	if err := json.Unmarshal(data, &sess); err != nil {
		return nil, err
	}
	return sess.History, nil
}

// List 列出所有会话 ID。
func List() ([]string, error) {
	dir, err := getSessionDir()
	if err != nil {
		return nil, err
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	var ids []string
	for _, entry := range entries {
		if !entry.IsDir() && filepath.Ext(entry.Name()) == ".json" {
			ids = append(ids, entry.Name()[:len(entry.Name())-5])
		}
	}
	return ids, nil
}
