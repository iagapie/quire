package app

import (
	"testing"

	"github.com/wailsapp/wails/v3/pkg/application"
)

func TestWindowStatePersistence(t *testing.T) {
	dir := t.TempDir()
	s := NewSettings(dir)
	if w, h := s.WindowSize(); w != DefaultWindowWidth || h != DefaultWindowHeight {
		t.Errorf("default size = %dx%d", w, h)
	}
	if _, ok := s.Window(); ok {
		t.Error("no window state expected before saving")
	}

	if err := s.SaveWindow(WindowState{X: 10, Y: 20, Width: 1200, Height: 800}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetTheme(ThemeDark); err != nil { // saving another setting keeps the window state
		t.Fatal(err)
	}
	again := NewSettings(dir)
	ws, ok := again.Window()
	if !ok || ws != (WindowState{X: 10, Y: 20, Width: 1200, Height: 800}) || again.Theme() != ThemeDark {
		t.Fatalf("reloaded = %+v, %v, theme %s", ws, ok, again.Theme())
	}

	// Sizes below the window minimum are raised to it.
	if err := s.SaveWindow(WindowState{Width: 100, Height: 50}); err != nil {
		t.Fatal(err)
	}
	if w, h := s.WindowSize(); w != MinWindowWidth || h != MinWindowHeight {
		t.Errorf("clamped size = %dx%d", w, h)
	}
}

func TestVisible(t *testing.T) {
	screens := []*application.Screen{
		{WorkArea: application.Rect{X: 0, Y: 25, Width: 1512, Height: 920}},
		{Bounds: application.Rect{X: 1512, Y: -200, Width: 2560, Height: 1440}}, // external monitor, no work area reported
	}
	tests := []struct {
		name string
		r    application.Rect
		want bool
	}{
		{"main screen", application.Rect{X: 100, Y: 100, Width: 1200, Height: 800}, true},
		{"external screen", application.Rect{X: 2000, Y: 0, Width: 1200, Height: 800}, true},
		{"mostly off the left edge", application.Rect{X: -1150, Y: 100, Width: 1200, Height: 800}, false},
		{"title bar above the screen", application.Rect{X: 100, Y: -500, Width: 1200, Height: 800}, false},
		{"disconnected monitor", application.Rect{X: -3000, Y: 100, Width: 1200, Height: 800}, false},
		{"below the screen", application.Rect{X: 100, Y: 2000, Width: 1200, Height: 800}, false},
	}
	for _, tt := range tests {
		if got := visible(tt.r, screens); got != tt.want {
			t.Errorf("%s: visible = %v, want %v", tt.name, got, tt.want)
		}
	}
	if visible(application.Rect{Width: 100, Height: 100}, nil) {
		t.Error("nothing is visible without screens")
	}
}
