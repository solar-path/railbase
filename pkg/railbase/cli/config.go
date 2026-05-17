package cli

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/railbase/railbase/internal/eventbus"
	"github.com/railbase/railbase/internal/settings"
)

// newConfigCmd assembles the `railbase config ...` subtree.
//
// `config get/set/list/delete` operate against the runtime-mutable
// `_settings` table. Boot-time configuration (config.go, env vars,
// flags) is reported by `config sources` to help operators reason
// about which layer wins.
func newConfigCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Manage runtime settings (UI-equivalent CLI)",
	}
	cmd.AddCommand(
		newConfigGetCmd(),
		newConfigSetCmd(),
		newConfigListCmd(),
		newConfigDeleteCmd(),
	)
	return cmd
}

func newConfigGetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "get <key>",
		Short: "Print the runtime value for <key> as JSON",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			mgr, cleanup, err := openSettingsManager(cmd)
			if err != nil {
				return err
			}
			defer cleanup()
			v, ok, err := mgr.Get(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			if !ok {
				return fmt.Errorf("config: %q is not set", args[0])
			}
			out, err := json.MarshalIndent(v, "", "  ")
			if err != nil {
				return err
			}
			fmt.Println(string(out))
			return nil
		},
	}
}

func newConfigSetCmd() *cobra.Command {
	var rawString bool
	cmd := &cobra.Command{
		Use:   "set <key> <value>",
		Short: "Write a runtime setting. Value is JSON by default; --string treats it as a literal string.",
		Long: strings.TrimSpace(`
Set a runtime configuration value.

By default the <value> argument is parsed as JSON:
  railbase config set mailer.smtp.port 587            # number
  railbase config set mailer.smtp.host '"localhost"'  # string (needs JSON quotes)
  railbase config set mailer.cc '["a@x","b@x"]'       # array
  railbase config set mailer.tls true                 # bool

If JSON parsing fails AND the value doesn't begin with one of the
JSON-significant characters ({, [, ", -, digit, t, f, n), the value
is treated as a literal string (FEEDBACK shopper #11). So the most
common case — a string setting — works without shell-quoting gymnastics:
  railbase config set mailer.smtp.host localhost

Pass --string to force string interpretation even when the value would
otherwise parse as valid JSON (e.g. setting a key to the literal
string "true").
`),
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			mgr, cleanup, err := openSettingsManager(cmd)
			if err != nil {
				return err
			}
			defer cleanup()
			val := args[1]
			var v any
			if rawString {
				v = val
			} else {
				// Try JSON first. If JSON fails AND the value clearly
				// isn't trying to be JSON (no leading {, [, ", -, digit,
				// t, f, n), fall back to literal string. This makes the
				// common case (string settings) painless without losing
				// the JSON power-mode.
				if err := json.Unmarshal([]byte(val), &v); err != nil {
					if looksLikeJSONStart(val) {
						return fmt.Errorf("config: value must be JSON (use --string for literal): %w", err)
					}
					v = val
				}
			}
			if err := mgr.Set(cmd.Context(), args[0], v); err != nil {
				return err
			}
			fmt.Printf("set %s\n", args[0])
			return nil
		},
	}
	cmd.Flags().BoolVar(&rawString, "string", false,
		"Treat <value> as a literal string (skip JSON parsing).")
	return cmd
}

// looksLikeJSONStart reports whether the value's first non-space byte
// matches a JSON-document start. Used by `config set` to decide
// whether a JSON parse failure is the operator's typo (return error)
// or a plain-text setting (silently accept). FEEDBACK shopper #11.
func looksLikeJSONStart(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	switch s[0] {
	case '{', '[', '"', '-':
		return true
	}
	if s[0] >= '0' && s[0] <= '9' {
		return true
	}
	switch s {
	case "true", "false", "null":
		return true
	}
	return false
}

func newConfigDeleteCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "delete <key>",
		Short: "Remove a runtime setting (falls back to defaults)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			mgr, cleanup, err := openSettingsManager(cmd)
			if err != nil {
				return err
			}
			defer cleanup()
			if err := mgr.Delete(cmd.Context(), args[0]); err != nil {
				return err
			}
			fmt.Printf("deleted %s\n", args[0])
			return nil
		},
	}
}

func newConfigListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "Print every settings key + value as JSON",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			mgr, cleanup, err := openSettingsManager(cmd)
			if err != nil {
				return err
			}
			defer cleanup()
			all, err := mgr.List(cmd.Context())
			if err != nil {
				return err
			}
			keys := make([]string, 0, len(all))
			for k := range all {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			out := make([][2]any, 0, len(keys))
			for _, k := range keys {
				out = append(out, [2]any{k, all[k]})
			}
			body, err := json.MarshalIndent(out, "", "  ")
			if err != nil {
				return err
			}
			fmt.Println(string(body))
			return nil
		},
	}
}

// openSettingsManager wraps openRuntime + applySysMigrations + builds
// a Manager pointed at the same pool. The eventbus is local to the
// CLI invocation — settings change events fire but no subscribers
// exist within the CLI process. That's fine; the bus stays as a
// stub so code paths match the serve-mode behaviour.
func openSettingsManager(cmd *cobra.Command) (*settings.Manager, func(), error) {
	rt, err := openRuntime(cmd.Context())
	if err != nil {
		return nil, nil, err
	}
	if err := applySysMigrations(cmd.Context(), rt); err != nil {
		rt.cleanup()
		return nil, nil, err
	}
	bus := eventbus.New(rt.log)
	mgr := settings.New(settings.Options{
		Pool: rt.pool.Pool,
		Bus:  bus,
		Log:  rt.log,
	})
	cleanup := func() {
		bus.Close()
		rt.cleanup()
	}
	return mgr, cleanup, nil
}
