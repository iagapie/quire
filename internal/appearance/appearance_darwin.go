//go:build darwin

// Package appearance switches the native light/dark appearance of the app.
package appearance

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework AppKit
#import <AppKit/AppKit.h>

static void setAppAppearance(int mode) {
	dispatch_async(dispatch_get_main_queue(), ^{
		NSAppearance *appearance = nil;
		if (mode == 1) {
			appearance = [NSAppearance appearanceNamed:NSAppearanceNameAqua];
		} else if (mode == 2) {
			appearance = [NSAppearance appearanceNamed:NSAppearanceNameDarkAqua];
		}
		[NSApp setAppearance:appearance];
	});
}
*/
import "C"

// Set applies "light", "dark" or "system" (follow macOS) to every window and native
// dialog of the application.
func Set(theme string) {
	switch theme {
	case "light":
		C.setAppAppearance(1)
	case "dark":
		C.setAppAppearance(2)
	default:
		C.setAppAppearance(0)
	}
}
