package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/j4n-e4t/rollout/internal/cli"
)

func main() {
	if err := cli.NewRoot().Execute(); err != nil {
		var code cli.ExitCode
		if errors.As(err, &code) {
			os.Exit(int(code))
		}
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
