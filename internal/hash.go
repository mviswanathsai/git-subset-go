package objecthash

import (
	"compress/zlib"
	"crypto/sha1"
	"errors"
	"fmt"
	"github.com/codecrafters-io/git-starter-go/main"
	files "github.com/codecrafters-io/git-starter-go/internal/files"
	"io"
	"io/fs"
	"os"
	fp "path/filepath"
)

func HashObject(filename string, write bool) {
	var tmpw io.Writer
	if write {
		tmpf := files.CreateTempObjFile()
		defer os.Remove(tmpf.Name())
		tmpw = tmpf
	} else {
		tmpw = io.Discard
	}

	f, finfo := files.OpenFile(filename)

	hash := sha1.New()
	zw := zlib.NewWriter(tmpw)
	mw := io.MultiWriter(hash, zw)
	files.WriteGitObject(mw, "blob", int(finfo.Size()), f)

	h := fmt.Sprintf("%x", hash.Sum(nil))
	zw.Close()

	if !write {
		// Print the hex encoding of the hash.
		fmt.Println(h)
		return
	}

	tmpf, ok := tmpw.(*os.File)
	if !ok {
		fmt.Fprintf(os.Stderr, "Fatal: tmpw is of unexpected type %T", tmpw)
		os.Exit(1)
	}
	tmpf.Close()

	objDirName, objFileName := decomposeHash(h)
	err := os.MkdirAll(fp.Join(gitObjDir, objDirName), 0775)
	if err != nil && !errors.Is(err, fs.ErrExist) {
		fmt.Fprintf(os.Stderr, "Error creating dir: %v\n", err)
		os.Exit(1)
	}

	objFilePath := fp.Join(gitObjDir, objDirName, objFileName)
	os.Rename(tmpf.Name(), objFilePath)
}
