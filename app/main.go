package main

import (
	"flag"
	"fmt"
	"os"
	"runtime"
	"runtime/pprof"
)

func main() {
	// 1. Define the profiling flags
	memProfile := flag.String("memprofile", "", "write memory profile to `file`")
	cpuProfile := flag.String("cpuprofile", "", "write cpu profile to `file`")

	// 2. We need to parse flags but leave the subcommands alone.
	// We'll parse only up to the first non-flag argument (the command).
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: mygit [-memprofile file] <command> [<args>]\n")
		flag.PrintDefaults()
	}
	flag.Parse()

	args := flag.Args()
	if len(args) < 1 {
		flag.Usage()
		os.Exit(1)
	}

	// 3. Handle CPU Profiling
	if *cpuProfile != "" {
		f, err := os.Create(*cpuProfile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "could not create CPU profile: %v\n", err)
			os.Exit(1)
		}
		defer f.Close()
		if err := pprof.StartCPUProfile(f); err != nil {
			fmt.Fprintf(os.Stderr, "could not start CPU profile: %v\n", err)
			os.Exit(1)
		}
		defer pprof.StopCPUProfile()
	}

	cmdName := args[0]
	cmdArgs := args[1:]

	command, ok := commands[cmdName]
	if !ok {
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n", cmdName)
		os.Exit(1)
	}

	// Execute the command
	if err := command.Run(cmdArgs); err != nil {
		fmt.Fprintf(os.Stderr, "error executing %s: %v\n", cmdName, err)
		os.Exit(1)
	}

	// 4. Handle Memory Profiling (Write at the very end to capture final heap state)
	if *memProfile != "" {
		f, err := os.Create(*memProfile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "could not create memory profile: %v\n", err)
			os.Exit(1)
		}
		defer f.Close()
		runtime.GC() // get up-to-date statistics
		if err := pprof.WriteHeapProfile(f); err != nil {
			fmt.Fprintf(os.Stderr, "could not write memory profile: %v\n", err)
			os.Exit(1)
		}
	}
}

