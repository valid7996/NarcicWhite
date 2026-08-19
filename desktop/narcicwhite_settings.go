package main

import (
	"fmt"

	"narcicwhite-desktop/internal/model"
)

// GetNarcicWhiteSettings returns the settings the phone app exposes.
func (a *App) GetNarcicWhiteSettings() model.NarcicWhiteSettings {
	a.mu.Lock()
	defer a.mu.Unlock()
	return settingsForThisMachine(model.NormalizeNarcicWhiteSettings(a.state.NarcicWhite))
}

// settingsForThisMachine drops what this build cannot do.
//
// Only the tunnel, and only where there is no way to raise the core. A machine
// that cannot start an elevated child cannot make an adapter, so the switch
// could never have worked there — and left stored as on it is unreachable, since
// the interface no longer offers the mode it belongs to. Every connection would
// fail with a sentence about an unimplemented function and no way to get out of
// it short of resetting the app.
//
// Deliberately not in model.NormalizeNarcicWhiteSettings: that repairs what a
// settings file says, which is the same answer everywhere, and giving it a
// dependency on the host would make its tests depend on which machine ran them.
func settingsForThisMachine(settings model.NarcicWhiteSettings) model.NarcicWhiteSettings {
	if settings.TunEnabled && !tunnelSupported() {
		settings.TunEnabled = false
	}
	return settings
}

// SaveNarcicWhiteSettings stores them, repaired.
//
// Normalising on the way in rather than trusting the form means a value that
// arrives out of range - from an edited state file, or a control that let
// something through - is corrected once, here, instead of reaching the engine
// and failing there where the cause is much harder to see.
func (a *App) SaveNarcicWhiteSettings(settings model.NarcicWhiteSettings) (model.AppState, error) {
	normalized := settingsForThisMachine(model.NormalizeNarcicWhiteSettings(settings))

	a.mu.Lock()
	a.state.NarcicWhite = normalized
	state, err := a.saveLocked()
	a.mu.Unlock()

	if err != nil {
		return state, fmt.Errorf("save settings: %w", err)
	}
	// The tray speaks the app's language too.
	a.notifyTray()
	return state, nil
}
