// Command p9s is a terminal UI for Kubernetes policy engines.
package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/yindia/p9s/cmd/p9s/app"
)

func main() {
	if err := app.NewRootCommand().Execute(); err != nil {
		var exit *app.ExitError
		if errors.As(err, &exit) {
			fmt.Fprintln(os.Stderr, exit.Error()) // a result such as --fail-on findings
			os.Exit(exit.Code)
		}
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
}
