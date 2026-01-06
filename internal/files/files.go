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

func OpenFile(filename string) (*os.File, os.FileInfo, error) {
	info, err := os.Stat(filename)
	if err != nil {
		return nil, nil, err
	}

	df, err := os.Open(filename)
	if err != nil {
		return nil, nil, err
	}
	return df, info, nil
}

// The caller is responsible for closing the writers as necessary.
func WriteGitObject(writer io.Writer, objType string, payloadSize int, payload io.Reader) error {
	header := fmt.Sprintf("%s %d\x00", objType, payloadSize)
	if _, err := writer.Write([]byte(header)); err != nil {
		return err
	}
	if _, err := io.Copy(writer, payload); err != nil {
		return err
	}
    return nil
}
