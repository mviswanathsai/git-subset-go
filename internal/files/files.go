package files

import (
	"fmt"
	git "github.com/codecrafters-io/git-starter-go/internal/git"
	"io"
	"os"
)

func CreateTempObjFile() *os.File {
	tmpf, err := os.CreateTemp(git.GitObjDir, "tmp_obj_")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating temp file: %v\n", err)
		os.Exit(1)
	}
	return tmpf
}

func OpenFile(filename string) (*os.File, os.FileInfo) {
	info, err := os.Stat(filename)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error fetching file info: %v\n", err)
		os.Exit(1)
	}

	df, err := os.Open(filename)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error reading file: %v\n", err)
		os.Exit(1)
	}
	return df, info
}

// The caller is responsible for closing the writers as necessary.
func WriteGitObject(writer io.Writer, objType string, payloadSize int, payload io.Reader) {
	header := fmt.Sprintf("%s %d\x00", objType, payloadSize)
	if _, err := writer.Write([]byte(header)); err != nil {
		fmt.Fprintf(os.Stderr, "Error writing object: %v\n", err)
	}
	if _, err := io.Copy(writer, payload); err != nil {
		fmt.Fprintf(os.Stderr, "Error writing object: %v\n", err)
	}
}
