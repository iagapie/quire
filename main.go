package main

import (
	"embed"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"

	"github.com/iagapie/quire/internal/app"
	_ "github.com/iagapie/quire/internal/dialect/all" // registers the database drivers
	"github.com/iagapie/quire/internal/docapp"
	"github.com/iagapie/quire/internal/docsession"
	"github.com/iagapie/quire/internal/profile"
	"github.com/iagapie/quire/internal/query"
)

//go:embed all:frontend/dist
var assets embed.FS

func main() {
	dir, err := configDir()
	if err != nil {
		log.Fatal(err)
	}
	store, err := openStore(dir)
	if err != nil {
		log.Fatal(err)
	}
	settings := app.NewSettings(dir)
	engine := query.New()
	docs := docsession.NewRegistry()
	conns := app.NewConnections(store, engine, docs)
	queries := app.NewQueries(conns, engine)
	transfers := app.NewTransfers(conns)
	docTransfers := docapp.NewTransfers(docs)

	a := application.New(application.Options{
		Name:        "Quire",
		Description: "Database editor for PostgreSQL, MySQL, SQL Server, Oracle, SQLite, Cosmos DB, MongoDB and DocumentDB",
		Services: []application.Service{
			application.NewService(conns),
			application.NewService(queries),
			application.NewService(transfers),
			application.NewService(docapp.NewShell(docs)),
			application.NewService(docTransfers),
			application.NewService(&app.Files{}),
			application.NewService(app.NewPreferences(settings)),
		},
		Assets: application.AssetOptions{
			Handler: application.AssetFileServerFS(assets),
		},
		Mac: application.MacOptions{
			ApplicationShouldTerminateAfterLastWindowClosed: true,
		},
		// Quitting with an open transaction asks the frontend to confirm first.
		ShouldQuit: queries.ShouldQuit,
	})

	a.Menu.SetApplicationMenu(appMenu(settings))

	width, height := settings.WindowSize()
	window := a.Window.NewWithOptions(application.WebviewWindowOptions{
		Title:     "Quire",
		Width:     width,
		Height:    height,
		MinWidth:  app.MinWindowWidth,
		MinHeight: app.MinWindowHeight,
		Mac: application.MacWindow{
			InvisibleTitleBarHeight: 38,
			Backdrop:                application.MacBackdropTranslucent,
			// A compact toolbar centres the window buttons in the 38px header row.
			TitleBar: application.MacTitleBar{
				AppearsTransparent:   true,
				HideTitle:            true,
				FullSizeContent:      true,
				UseToolbar:           true,
				HideToolbarSeparator: true,
				ToolbarStyle:         application.MacToolbarStyleUnifiedCompact,
			},
		},
		BackgroundColour: settings.WindowBackground(a.Env.IsDarkMode()),
		URL:              "/",
		EnableFileDrop:   true,
	})
	// Dump files dropped onto an import tab are added to it. Import tabs of MongoDB
	// connections have drop targets named "doc-import-…".
	window.OnWindowEvent(events.Common.WindowFilesDropped, func(e *application.WindowEvent) {
		target := ""
		if d := e.Context().DropTargetDetails(); d != nil {
			target = d.ElementID
		}
		if strings.HasPrefix(target, "doc-import-") {
			docTransfers.FilesDropped(e.Context().DroppedFiles(), target)
			return
		}
		transfers.FilesDropped(e.Context().DroppedFiles(), target)
	})
	// Closing the window with a transaction open asks for confirmation too.
	window.RegisterHook(events.Common.WindowClosing, func(e *application.WindowEvent) {
		if !queries.ShouldQuit() {
			e.Cancel()
		}
	})

	a.Event.OnApplicationEvent(events.Common.ApplicationStarted, func(*application.ApplicationEvent) {
		settings.Apply()
		app.RestoreWindow(a, window, settings)
		app.TrackWindow(window, settings)
	})
	// With the "system" preference the window colour follows macOS.
	a.Event.OnApplicationEvent(events.Common.ThemeChanged, func(*application.ApplicationEvent) {
		settings.Apply()
	})

	if err := a.Run(); err != nil {
		log.Fatal(err)
	}
}

// eventToggleSidebar asks the frontend to show or hide the sidebar (View menu).
const eventToggleSidebar = "view:toggle-sidebar"

func init() {
	application.RegisterEvent[string](eventToggleSidebar)
}

func appMenu(settings *app.Settings) *application.Menu {
	menu := application.NewMenu()
	if runtime.GOOS == "darwin" {
		menu.AddRole(application.AppMenu)
	}
	menu.AddRole(application.EditMenu)
	view := menu.AddSubmenu("View")
	view.Add("Toggle Sidebar").SetAccelerator("CmdOrCtrl+B").OnClick(func(*application.Context) {
		application.Get().Event.Emit(eventToggleSidebar, "")
	})
	view.AddSeparator()
	settings.AppearanceMenu(view.AddSubmenu("Appearance"))
	view.AddSeparator()
	view.AddRole(application.ResetZoom)
	view.AddRole(application.ZoomIn)
	view.AddRole(application.ZoomOut)
	view.AddSeparator()
	view.AddRole(application.ToggleFullscreen)
	menu.AddRole(application.WindowMenu)
	return menu
}

// configDir is where connections and settings live. QUIRE_CONFIG_DIR overrides it.
func configDir() (string, error) {
	if dir := os.Getenv("QUIRE_CONFIG_DIR"); dir != "" {
		return dir, nil
	}
	cfg, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(cfg, "quire"), nil
}

// openStore opens the profile store. QUIRE_SECRETS=file keeps passwords in the
// config directory instead of the OS keychain.
func openStore(dir string) (*profile.Store, error) {
	if os.Getenv("QUIRE_SECRETS") == "file" {
		return profile.NewFileStore(dir)
	}
	return profile.NewStore(dir)
}
