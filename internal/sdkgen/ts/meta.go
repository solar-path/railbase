package ts

import (
	"encoding/json"
	"fmt"

	"github.com/railbase/railbase/internal/sdkgen"
)

// metaJSON renders the Meta value as a 2-space-indented JSON document
// with a trailing newline. Pretty-printed because it ends up in the
// user's repo and they read it.
func metaJSON(m sdkgen.Meta) ([]byte, error) {
	out, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("ts.metaJSON: %w", err)
	}
	return append(out, '\n'), nil
}
