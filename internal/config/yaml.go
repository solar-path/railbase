package config

// Phase 3.x — minimal `railbase.yaml` parser. Closes the "yaml is a
// no-op" papercut Sentinel's `railbase.yaml:4` comment surfaces:
//
//	# v0.2 reads only env vars. This file is here as documentation for
//	# the v0.3+ yaml-config layer; setting values here today is a no-op.
//
// Scope: only the operator-facing knobs that already exist as
// RAILBASE_* env vars. We don't grow the config surface area in this
// patch — the goal is to make the existing surface available via a
// second source (yaml file) at a lower precedence than env, so the
// 12-factor / GitOps deployment model works.
//
// Precedence (highest wins):
//
//	1. CLI flags (applied by callers after Load returns)
//	2. Process env (RAILBASE_*)
//	3. .env files
//	4. railbase.yaml file
//	5. Default() bake-ins
//
// File lookup: `./railbase.yaml`, then `<DataDir>/railbase.yaml`.
// First match wins (we do NOT merge multiple files — keeps the
// model simple and predictable).
//
// Unknown fields are warnings, not errors — keeps yaml additions
// from breaking older binaries, mirrors `.env` behaviour.

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// yamlConfig is the on-disk shape. Mirrors the env var names but in
// nested form (operators expect `http.addr` not `HTTP_ADDR`). Every
// field is a pointer so we can tell "unset" from "set to zero" —
// only set fields override Defaults / earlier sources.
type yamlConfig struct {
	HTTP    *yamlHTTPSection    `yaml:"http,omitempty"`
	DB      *yamlDBSection      `yaml:"db,omitempty"`
	Log     *yamlLogSection     `yaml:"log,omitempty"`
	Data    *yamlDataSection    `yaml:"data,omitempty"`
	Hooks   *yamlHooksSection   `yaml:"hooks,omitempty"`
	Public  *yamlPublicSection  `yaml:"public,omitempty"`
	Runtime *yamlRuntimeSection `yaml:"runtime,omitempty"`
}

type yamlHTTPSection struct {
	Addr          *string `yaml:"addr,omitempty"`
	ShutdownGrace *string `yaml:"shutdown_grace,omitempty"`
	PBCompat      *string `yaml:"pb_compat,omitempty"`
}

type yamlDBSection struct {
	DSN           *string `yaml:"dsn,omitempty"`
	EmbedPostgres *bool   `yaml:"embed_postgres,omitempty"`
}

type yamlLogSection struct {
	Level  *string `yaml:"level,omitempty"`
	Format *string `yaml:"format,omitempty"`
}

type yamlDataSection struct {
	Dir *string `yaml:"dir,omitempty"`
}

type yamlHooksSection struct {
	Dir *string `yaml:"dir,omitempty"`
}

type yamlPublicSection struct {
	Dir *string `yaml:"dir,omitempty"`
}

type yamlRuntimeSection struct {
	Production *bool `yaml:"production,omitempty"`
	Dev        *bool `yaml:"dev,omitempty"`
}

// loadYAMLConfig reads the first `railbase.yaml` it finds at the
// given lookup paths and applies set fields to c. Returns (foundPath,
// err) so the caller can log "loaded from ./railbase.yaml". Missing
// files are not errors — the function is a no-op for default deploys
// that don't use the yaml layer.
//
// Unrecognised yaml keys produce a warning written to stderr; the
// load continues. Schema-level malformations (e.g. addr is a number
// not a string) ARE returned as errors — silent type-coercion would
// hide configuration bugs.
func loadYAMLConfig(c *Config, lookupPaths []string) (string, error) {
	for _, p := range lookupPaths {
		body, err := os.ReadFile(p)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", fmt.Errorf("read %s: %w", p, err)
		}
		var yc yamlConfig
		dec := yaml.NewDecoder(bytes.NewReader(body))
		dec.KnownFields(true) // tight schema; typo = error
		if err := dec.Decode(&yc); err != nil {
			// Soften "unknown field" to a warning; bail on hard errors.
			if isUnknownFieldError(err) {
				fmt.Fprintf(os.Stderr, "railbase: %s: %v (continuing)\n", p, err)
				// Reparse without KnownFields so the rest of the file applies.
				dec2 := yaml.NewDecoder(bytes.NewReader(body))
				if err2 := dec2.Decode(&yc); err2 != nil {
					return p, fmt.Errorf("parse %s: %w", p, err2)
				}
			} else {
				return p, fmt.Errorf("parse %s: %w", p, err)
			}
		}
		if err := applyYAML(c, yc); err != nil {
			return p, fmt.Errorf("apply %s: %w", p, err)
		}
		return p, nil
	}
	return "", nil // no file found — no-op
}

// applyYAML overlays set fields from yc onto c. Skipped fields (nil
// pointers) leave the existing value untouched, so a yaml file that
// sets only http.addr doesn't reset every other field to its zero.
func applyYAML(c *Config, yc yamlConfig) error {
	if yc.HTTP != nil {
		if yc.HTTP.Addr != nil {
			c.HTTPAddr = *yc.HTTP.Addr
		}
		if yc.HTTP.ShutdownGrace != nil {
			d, err := time.ParseDuration(*yc.HTTP.ShutdownGrace)
			if err != nil {
				return fmt.Errorf("http.shutdown_grace: %w", err)
			}
			c.ShutdownGrace = d
		}
		if yc.HTTP.PBCompat != nil {
			c.PBCompat = *yc.HTTP.PBCompat
		}
	}
	if yc.DB != nil {
		if yc.DB.DSN != nil {
			c.DSN = *yc.DB.DSN
		}
		if yc.DB.EmbedPostgres != nil {
			c.EmbedPostgres = *yc.DB.EmbedPostgres
		}
	}
	if yc.Log != nil {
		if yc.Log.Level != nil {
			c.LogLevel = *yc.Log.Level
		}
		if yc.Log.Format != nil {
			c.LogFormat = *yc.Log.Format
		}
	}
	if yc.Data != nil && yc.Data.Dir != nil {
		c.DataDir = *yc.Data.Dir
	}
	if yc.Hooks != nil && yc.Hooks.Dir != nil {
		c.HooksDir = *yc.Hooks.Dir
	}
	if yc.Public != nil && yc.Public.Dir != nil {
		c.PublicDir = *yc.Public.Dir
	}
	if yc.Runtime != nil {
		if yc.Runtime.Production != nil {
			c.ProductionMode = *yc.Runtime.Production
		}
		if yc.Runtime.Dev != nil {
			c.DevMode = *yc.Runtime.Dev
		}
	}
	return nil
}

// yamlLookupPaths returns the file paths Load() will probe, in order.
// CWD first (project root convention), then DataDir (operator-managed
// secrets / runtime dir convention).
func yamlLookupPaths(dataDir string) []string {
	return []string{
		"railbase.yaml",
		"railbase.yml",
		filepath.Join(dataDir, "railbase.yaml"),
		filepath.Join(dataDir, "railbase.yml"),
	}
}

// isUnknownFieldError detects yaml.v3's "field X not found in type Y"
// error so we can downgrade it to a warning. The library doesn't
// expose a typed error — match by message substring.
func isUnknownFieldError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "not found in type") ||
		strings.Contains(msg, "field ") ||
		strings.Contains(msg, "unknown field")
}
