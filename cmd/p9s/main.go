// Command p9s is a terminal UI for Kubernetes policy engines.
package main

import (
	"errors"
	"os"

	"github.com/yindia/p9s/cmd/p9s/app"
)

func main() {
	if err := app.NewRootCommand().Execute(); err != nil {
		var exit *app.ExitError
		if errors.As(err, &exit) {
			os.Exit(exit.Code)
		}
		os.Exit(1)
	}
}
