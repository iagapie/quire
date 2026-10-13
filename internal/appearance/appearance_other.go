//go:build !darwin

// Package appearance switches the native light/dark appearance of the app.
package appearance

// Set is a no-op outside macOS; the web content follows the theme on its own.
func Set(string) {}
