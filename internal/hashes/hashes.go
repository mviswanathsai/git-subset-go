package hashes
// This pkg needn't exist. I am keeping it because I don't wanna rename multiple lines of code.
import (
	"compress/zlib"
	"crypto/sha1"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	fp "path/filepath"

	files "github.com/codecrafters-io/git-starter-go/internal/files"
	git "github.com/codecrafters-io/git-starter-go/internal/git"
)

func HashAndWriteObject(f io.Reader, size int64, objType string, write bool) (string, error) {
	var tmpw io.Writer
	if write {
		tmpf, err := files.CreateTempObjFile()
        if err != nil {
            return "", err
        }
		defer os.Remove(tmpf.Name())
		tmpw = tmpf
	} else {
		tmpw = io.Discard
	}

	hash := sha1.New()
	zw := zlib.NewWriter(tmpw)
	mw := io.MultiWriter(hash, zw)
	if err := files.WriteGitObject(mw, objType, int(size), f); err != nil {
		return "", fmt.Errorf("Error writing object to disk")
	}

	h := fmt.Sprintf("%x", hash.Sum(nil))
	zw.Close()

	if !write {
		return h, nil
	}

	tmpf, ok := tmpw.(*os.File)
	if !ok {
		return "", fmt.Errorf("Fatal: tmpw is of unexpected type %T\n", tmpw)
	}
	if err := tmpf.Close(); err != nil {
		return "", err
	}

	objDirName, objFileName := DecomposeHash(h)
	err := os.MkdirAll(fp.Join(git.GitObjDir, objDirName), 0775)
	if err != nil && !errors.Is(err, fs.ErrExist) {
		fmt.Fprintf(os.Stderr, "Error creating dir: %v\n", err)
		os.Exit(1)
	}

	objFilePath := fp.Join(git.GitObjDir, objDirName, objFileName)
    if err := os.Rename(tmpf.Name(), objFilePath); err != nil {
        return "", fmt.Errorf("Error creating object file: %w\n", err)
    }
	return h, nil
}

func DecomposeHash(hash string) (dirName, fileName string) {
	return hash[:2], hash[2:]
}
