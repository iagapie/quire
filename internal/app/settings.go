package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/wailsapp/wails/v3/pkg/application"

	"github.com/iagapie/quire/internal/appearance"
)

// EventTheme is emitted with the new theme preference whenever it changes.
const EventTheme = "theme:changed"

func init() {
	application.RegisterEvent[string](EventTheme)
}

// Theme preferences.
const (
	ThemeSystem = "system"
	ThemeLight  = "light"
	ThemeDark   = "dark"
)

// Window background colours matching the frontend --bg tokens, so no wrong colour
// flashes before the page paints.
var (
	bgLight = application.NewRGB(251, 251, 252)
	bgDark  = application.NewRGB(22, 22, 24)
)

// Settings stores app preferences in settings.json next to the connections.
type Settings struct {
	path string

	mu     sync.Mutex
	theme  string
	window *WindowState
	radios map[string]*application.MenuItem
}

type settingsFile struct {
	Theme  string       `json:"theme"`
	Window *WindowState `json:"window,omitempty"`
}

// NewSettings loads dir/settings.json; a missing or invalid file yields defaults.
func NewSettings(dir string) *Settings {
	s := &Settings{path: filepath.Join(dir, "settings.json"), theme: ThemeSystem}
	if data, err := os.ReadFile(s.path); err == nil {
		var f settingsFile
		if json.Unmarshal(data, &f) == nil {
			if validTheme(f.Theme) {
				s.theme = f.Theme
			}
			s.window = f.Window
		}
	}
	return s
}

func validTheme(t string) bool {
	return t == ThemeSystem || t == ThemeLight || t == ThemeDark
}

// Theme returns the theme preference.
func (s *Settings) Theme() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.theme
}

// SetTheme stores the theme preference and applies it to the native window and menu.
func (s *Settings) SetTheme(theme string) error {
	if !validTheme(theme) {
		return fmt.Errorf("unknown theme %q", theme)
	}
	s.mu.Lock()
	changed := s.theme != theme
	s.theme = theme
	err := s.save()
	s.mu.Unlock()
	s.Apply()
	if changed {
		emit(EventTheme, theme)
	}
	return err
}

// WindowBackground is the window colour for the current preference.
func (s *Settings) WindowBackground(systemDark bool) application.RGBA {
	if s.isDark(systemDark) {
		return bgDark
	}
	return bgLight
}

func (s *Settings) isDark(systemDark bool) bool {
	switch s.Theme() {
	case ThemeDark:
		return true
	case ThemeLight:
		return false
	}
	return systemDark
}

// Apply pushes the preference to the native appearance, window backgrounds and menu.
func (s *Settings) Apply() {
	theme := s.Theme()
	appearance.Set(theme)
	app := application.Get()
	if app == nil {
		return
	}
	bg := s.WindowBackground(app.Env.IsDarkMode())
	for _, w := range app.Window.GetAll() {
		w.SetBackgroundColour(bg)
	}
	s.mu.Lock()
	radios := s.radios
	s.mu.Unlock()
	for t, item := range radios {
		item.SetChecked(t == theme)
	}
	if len(radios) > 0 {
		if menu := app.Menu.GetApplicationMenu(); menu != nil {
			menu.Update()
		}
	}
}

// AppearanceMenu adds System/Light/Dark radio items to menu.
func (s *Settings) AppearanceMenu(menu *application.Menu) {
	radios := map[string]*application.MenuItem{}
	current := s.Theme()
	for _, opt := range []struct{ theme, label string }{
		{ThemeSystem, "System"},
		{ThemeLight, "Light"},
		{ThemeDark, "Dark"},
	} {
		theme := opt.theme
		radios[theme] = menu.AddRadio(opt.label, theme == current).OnClick(func(*application.Context) {
			_ = s.SetTheme(theme)
		})
	}
	s.mu.Lock()
	s.radios = radios
	s.mu.Unlock()
}

func (s *Settings) save() error {
	data, err := json.MarshalIndent(settingsFile{Theme: s.theme, Window: s.window}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return errors.Join(errors.New("save settings"), err)
	}
	return os.Rename(tmp, s.path)
}

// Preferences is the frontend-facing settings service.
type Preferences struct {
	settings *Settings
}

// NewPreferences wraps settings for binding.
func NewPreferences(s *Settings) *Preferences { return &Preferences{settings: s} }

// GetTheme returns "system", "light" or "dark".
func (p *Preferences) GetTheme() string { return p.settings.Theme() }

// SetTheme changes the theme preference.
func (p *Preferences) SetTheme(theme string) error { return p.settings.SetTheme(theme) }
