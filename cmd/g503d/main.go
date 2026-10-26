package main

import (
	"fmt"
	"os"

	"github.com/yanagihalab/G503/cmd/g503d/cmd"
)

func main() {
	rootCmd := cmd.NewRootCmd()
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
