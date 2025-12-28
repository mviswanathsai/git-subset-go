package hashes

import (
	"compress/zlib"
	"crypto/sha1"
	"errors"
	"fmt"
	files "github.com/codecrafters-io/git-starter-go/internal/files"
	git "github.com/codecrafters-io/git-starter-go/internal/git"
	"io"
	"io/fs"
	"os"
	fp "path/filepath"
)

func HashObject(f io.Reader, size int64, objType string, write bool) string {
	var tmpw io.Writer
	if write {
		tmpf := files.CreateTempObjFile()
		defer os.Remove(tmpf.Name())
		tmpw = tmpf
	} else {
		tmpw = io.Discard
	}

	hash := sha1.New()
	zw := zlib.NewWriter(tmpw)
	mw := io.MultiWriter(hash, zw)
	files.WriteGitObject(mw, objType, int(size), f)

	h := fmt.Sprintf("%x", hash.Sum(nil))
	zw.Close()

	if !write {
		return h
	}

	tmpf, ok := tmpw.(*os.File)
	if !ok {
		fmt.Fprintf(os.Stderr, "Fatal: tmpw is of unexpected type %T", tmpw)
		os.Exit(1)
	}
	tmpf.Close()

	objDirName, objFileName := DecomposeHash(h)
	err := os.MkdirAll(fp.Join(git.GitObjDir, objDirName), 0775)
	if err != nil && !errors.Is(err, fs.ErrExist) {
		fmt.Fprintf(os.Stderr, "Error creating dir: %v\n", err)
		os.Exit(1)
	}

	objFilePath := fp.Join(git.GitObjDir, objDirName, objFileName)
	os.Rename(tmpf.Name(), objFilePath)
	return h
}

func DecomposeHash(hash string) (dirName, fileName string) {
	return hash[:2], hash[2:]
}
