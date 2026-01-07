package main

import (
	"fmt"
	"os"
)

const (
	OP_CODE_COPY   = 1
	OP_COPE_INSERT = 0
)

// Usage: your_program.sh <command> <arg1> <arg2> ...
func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "usage: mygit <command> [<args>]\n")
		os.Exit(1)
	}

	cmdName := os.Args[1]
	cmdArgs := os.Args[2:]

	command, ok := commands[cmdName]
	if !ok {
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n", cmdName)
		os.Exit(1)
	}

	if err := command.Run(cmdArgs); err != nil {
		fmt.Fprintf(os.Stderr, "error executing %s: %v\n", cmdName, err)
		os.Exit(1)
	}
}
