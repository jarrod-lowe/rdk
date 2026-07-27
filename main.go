package main

import (
	"os"

	"github.com/jarrod-lowe/rdk/cmd"
)

func main() {
	if err := cmd.NewRootCmd().Execute(); err != nil {
		os.Exit(1)
	}
}
