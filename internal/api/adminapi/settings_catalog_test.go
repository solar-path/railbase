package adminapi

// Unit tests for the v1.x typed settings catalog.
//
// The catalog is the contract between the Go consumer (readSetting
// at boot) and the SPA's typed General settings screen. Drift is the
// failure mode we care about — a key declared in the catalog but no
// consumer reads it (operator wastes a save), or a consumer reads a
// key that's not in the catalog (the operator has no UI to set it).
//
// This test enforces two invariants:
//
//   1. Every catalog entry references a `Group` that's listed in
//      `settingsGroups`. A stray group name renders to nothing in
//      the SPA.
//
//   2. Every catalog entry has a Description. A blank description
//      means the operator stares at a form field with no clue what
//      it does.
//
// Catalog ↔ consumer drift is checked separately by the lint pass
// (see scripts/lint_settings_catalog.sh — when one lands); the unit
// tests can't grep the live binary.

import (
	"strings"
	"testing"
)

func TestSettingsCatalog_EveryGroupExists(t *testing.T) {
	known := make(map[string]struct{}, len(settingsGroups))
	for _, g := range settingsGroups {
		known[g] = struct{}{}
	}
	for _, def := range settingsCatalog {
		if _, ok := known[def.Group]; !ok {
			t.Errorf("setting %q references unknown group %q; valid groups: %v",
				def.Key, def.Group, settingsGroups)
		}
	}
}

func TestSettingsCatalog_EveryEntryHasDescription(t *testing.T) {
	for _, def := range settingsCatalog {
		if strings.TrimSpace(def.Description) == "" {
			t.Errorf("setting %q has no description — operators won't know what it does",
				def.Key)
		}
		if strings.TrimSpace(def.Label) == "" {
			t.Errorf("setting %q has no label — the form row renders blank",
				def.Key)
		}
	}
}

func TestSettingsCatalog_KeysAreUnique(t *testing.T) {
	seen := make(map[string]struct{}, len(settingsCatalog))
	for _, def := range settingsCatalog {
		if _, dup := seen[def.Key]; dup {
			t.Errorf("duplicate catalog key %q — the SPA would render two form rows for the same setting", def.Key)
		}
		seen[def.Key] = struct{}{}
	}
}

func TestSettingsCatalog_ReloadIsSet(t *testing.T) {
	// Every entry MUST declare a reload posture — leaving it empty
	// would silently fall through to "" which the SPA can't render
	// (no badge, operator gets the pre-fix "looks saved but nothing
	// happened" UX back).
	for _, def := range settingsCatalog {
		switch def.Reload {
		case SettingReloadLive, SettingReloadRestart:
			// ok
		default:
			t.Errorf("setting %q has empty/unknown Reload value %q — declare it explicitly",
				def.Key, def.Reload)
		}
	}
}

func TestSettingsCatalog_TypeIsKnown(t *testing.T) {
	known := map[SettingType]struct{}{
		SettingTypeString:   {},
		SettingTypeBool:     {},
		SettingTypeInt:      {},
		SettingTypeCSV:      {},
		SettingTypeDuration: {},
		SettingTypeJSON:     {},
	}
	for _, def := range settingsCatalog {
		if _, ok := known[def.Type]; !ok {
			t.Errorf("setting %q has unknown type %q — the SPA can't render a form control for it",
				def.Key, def.Type)
		}
	}
}
