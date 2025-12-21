package main

import (
	"bytes"
	"compress/zlib"
	"fmt"
	"io"
	"os"
	fp "path/filepath"
)

const (
	objdir = ".git/objects"
)

// Usage: your_program.sh <command> <arg1> <arg2> ...
func main() {
	// You can use print statements as follows for debugging, they'll be visible when running tests.
	fmt.Fprintf(os.Stderr, "Logs from your program will appear here!\n")

	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "usage: mygit <command> [<args>...]\n")
		os.Exit(1)
	}

	switch command := os.Args[1]; command {
	case "init":
		for _, dir := range []string{".git", ".git/objects", ".git/refs"} {
			if err := os.MkdirAll(dir, 0755); err != nil {
				fmt.Fprintf(os.Stderr, "Error creating directory: %s\n", err)
			}
		}

		headFileContents := []byte("ref: refs/heads/main\n")
		if err := os.WriteFile(".git/HEAD", headFileContents, 0644); err != nil {
			fmt.Fprintf(os.Stderr, "Error writing file: %s\n", err)
		}

		fmt.Println("Initialized git directory")

	case "cat-file":
		objhash := os.Args[3]
		blobdir := objhash[:2]
		blobname := objhash[2:]

		// we first need to open the file
		filename := fp.Join(objdir, blobdir, blobname)
		f, err := os.Open(filename)
		if err != nil {
			fmt.Print(err)
			return
		}

		var b bytes.Buffer
		r, err := zlib.NewReader(f)
		if err != nil {
			fmt.Print(err)
			return
		}
		io.Copy(&b, r)
		r.Close()

		// advance the cursor after header
		_, err = b.ReadBytes(0)
		if err != nil {
			fmt.Print(err)
			return
		}

		fmt.Print(b.String())

	default:
		fmt.Fprintf(os.Stderr, "Unknown command %s\n", command)
		os.Exit(1)
	}
}
