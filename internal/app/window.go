package app

import (
	"sync"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

// WindowState is the saved geometry of the main window (DIP, absolute coordinates).
type WindowState struct {
	X         int  `json:"x"`
	Y         int  `json:"y"`
	Width     int  `json:"width"`
	Height    int  `json:"height"`
	Maximised bool `json:"maximised"`
}

// Window size limits, shared with the window options.
const (
	DefaultWindowWidth  = 1440
	DefaultWindowHeight = 900
	MinWindowWidth      = 900
	MinWindowHeight     = 560
)

// WindowSize returns the size to create the window with.
func (s *Settings) WindowSize() (width, height int) {
	ws, ok := s.Window()
	if !ok {
		return DefaultWindowWidth, DefaultWindowHeight
	}
	return max(ws.Width, MinWindowWidth), max(ws.Height, MinWindowHeight)
}

// Window returns the saved window geometry, if any.
func (s *Settings) Window() (WindowState, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.window == nil || s.window.Width <= 0 || s.window.Height <= 0 {
		return WindowState{}, false
	}
	return *s.window, true
}

// SaveWindow persists the window geometry.
func (s *Settings) SaveWindow(ws WindowState) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.window = &ws
	return s.save()
}

// visible reports whether enough of r is on one of the screens to grab the window.
func visible(r application.Rect, screens []*application.Screen) bool {
	const minVisible = 80
	for _, sc := range screens {
		a := sc.WorkArea
		if a.Width == 0 || a.Height == 0 {
			a = sc.Bounds
		}
		left, right := max(r.X, a.X), min(r.X+r.Width, a.X+a.Width)
		top, bottom := max(r.Y, a.Y), min(r.Y+r.Height, a.Y+a.Height)
		// The title bar (top of the window) must be reachable.
		if right-left >= minVisible && r.Y >= a.Y-10 && r.Y < a.Y+a.Height-minVisible/2 && bottom-top > 0 {
			return true
		}
	}
	return false
}

// RestoreWindow applies the saved geometry when it is still on a connected screen;
// otherwise the window keeps its default, centred placement.
func RestoreWindow(app *application.App, w *application.WebviewWindow, s *Settings) {
	ws, ok := s.Window()
	if !ok {
		return
	}
	rect := application.Rect{X: ws.X, Y: ws.Y, Width: max(ws.Width, MinWindowWidth), Height: max(ws.Height, MinWindowHeight)}
	if visible(rect, app.Screen.GetAll()) {
		w.SetBounds(rect)
	}
	if ws.Maximised {
		w.Maximise()
	}
}

// TrackWindow saves the window geometry whenever it is moved or resized (debounced)
// and when it closes. While maximised or fullscreen only that flag changes, so the
// normal size is restored after un-maximising.
func TrackWindow(w *application.WebviewWindow, s *Settings) {
	var (
		mu    sync.Mutex
		timer *time.Timer
	)
	save := func() {
		if w.IsMinimised() || w.IsFullscreen() {
			return
		}
		ws, _ := s.Window()
		if w.IsMaximised() {
			ws.Maximised = true
		} else {
			b := w.Bounds()
			if b.Width <= 0 || b.Height <= 0 {
				return
			}
			ws = WindowState{X: b.X, Y: b.Y, Width: b.Width, Height: b.Height}
		}
		_ = s.SaveWindow(ws)
	}
	schedule := func(*application.WindowEvent) {
		mu.Lock()
		defer mu.Unlock()
		if timer != nil {
			timer.Stop()
		}
		timer = time.AfterFunc(400*time.Millisecond, save)
	}
	w.OnWindowEvent(events.Common.WindowDidResize, schedule)
	w.OnWindowEvent(events.Common.WindowDidMove, schedule)
	w.RegisterHook(events.Common.WindowClosing, func(*application.WindowEvent) {
		mu.Lock()
		if timer != nil {
			timer.Stop()
		}
		mu.Unlock()
		save()
	})
}
