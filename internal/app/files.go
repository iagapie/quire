package app

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// ScriptFile is a script loaded from disk.
type ScriptFile struct {
	Path    string `json:"path"`
	Name    string `json:"name"`
	Content string `json:"content"`
}

// Files loads and saves scripts and picks files for connection settings.
type Files struct{}

const maxScriptSize = 10 << 20

// Script kinds, by the editor that loads or saves them.
const (
	scriptSQL   = "sql"
	scriptShell = "shell" // mongo shell JavaScript
)

// OpenScript asks for a script file of a kind ("sql" or "shell") and returns its
// content. An empty path means the user cancelled.
func (Files) OpenScript(kind string) (ScriptFile, error) {
	dlg := application.Get().Dialog.OpenFile().SetTitle("Load script")
	if kind == scriptShell {
		dlg.AddFilter("MongoDB scripts", "*.js;*.mongodb;*.mongodb.js;*.txt")
	} else {
		dlg.AddFilter("SQL scripts", "*.sql;*.txt")
	}
	path, err := dlg.AddFilter("All files", "*").PromptForSingleSelection()
	if err != nil || path == "" {
		return ScriptFile{}, err
	}
	return readScript(path)
}

func readScript(path string) (ScriptFile, error) {
	info, err := os.Stat(path)
	if err != nil {
		return ScriptFile{}, err
	}
	if info.Size() > maxScriptSize {
		return ScriptFile{}, errors.New("file is larger than 10 MB")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ScriptFile{}, fmt.Errorf("read script: %w", err)
	}
	return ScriptFile{Path: path, Name: filepath.Base(path), Content: string(data)}, nil
}

// SaveScript writes content to path, asking for a location when path is empty or saveAs
// is set. kind ("sql" or "shell") picks the file type offered. It returns the path
// written, or "" if the user cancelled.
func (Files) SaveScript(kind, path, content string, saveAs bool) (string, error) {
	if path == "" || saveAs {
		name, label, pattern := "script.sql", "SQL scripts", "*.sql"
		if kind == scriptShell {
			name, label, pattern = "script.js", "MongoDB scripts", "*.js;*.mongodb"
		}
		if path != "" {
			name = filepath.Base(path)
		}
		dlg := application.Get().Dialog.SaveFile().SetFilename(name).AddFilter(label, pattern)
		if path != "" {
			dlg.SetDirectory(filepath.Dir(path))
		}
		p, err := dlg.PromptForSingleSelection()
		if err != nil || p == "" {
			return "", err
		}
		path = p
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		return "", fmt.Errorf("save script: %w", err)
	}
	return path, nil
}

// PickDatabaseFile asks for a database file (SQLite); create allows a new file name.
func (Files) PickDatabaseFile(create bool) (string, error) {
	if create {
		return application.Get().Dialog.SaveFile().SetFilename("database.db").PromptForSingleSelection()
	}
	return application.Get().Dialog.OpenFile().
		SetTitle("Open database").
		AddFilter("SQLite databases", "*.db;*.sqlite;*.sqlite3;*.db3").
		AddFilter("All files", "*").
		PromptForSingleSelection()
}

// PickFile asks for any file (CA bundle, SSH key) and returns its path.
func (Files) PickFile(title string) (string, error) {
	dlg := application.Get().Dialog.OpenFile().SetTitle(title).ShowHiddenFiles(true)
	if home, err := os.UserHomeDir(); err == nil {
		if _, err := os.Stat(filepath.Join(home, ".ssh")); err == nil && title == "SSH private key" {
			dlg.SetDirectory(filepath.Join(home, ".ssh"))
		}
	}
	return dlg.PromptForSingleSelection()
}
