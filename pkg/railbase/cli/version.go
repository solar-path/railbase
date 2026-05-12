package cli

import (
	"fmt"

	"github.com/railbase/railbase/internal/buildinfo"
	"github.com/spf13/cobra"
)

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version information",
		Run: func(_ *cobra.Command, _ []string) {
			fmt.Println(buildinfo.String())
		},
	}
}
