package main

import (
	"compress/zlib"
	"fmt"
	"io"
	"os"
	fp "path/filepath"

	"github.com/codecrafters-io/git-starter-go/internal/git"
	"github.com/codecrafters-io/git-starter-go/internal/hashes"
)

type ObjectStorer struct {
	objDir  string
	zw      *zlib.Writer
	hashMap map[string]*ResolvedObject
}

func NewObjectStorer() *ObjectStorer {
	return &ObjectStorer{
		objDir:  git.GitObjDir,
		zw:      zlib.NewWriter(nil),
		hashMap: make(map[string]*ResolvedObject),
	}
}

// Store is our Callback implementation
func (s *ObjectStorer) Store(obj *ResolvedObject) error {
	dirName, fileName := hashes.DecomposeHash(obj.SHA1)
	fullDir := fp.Join(s.objDir, dirName)

	s.hashMap[obj.SHA1] = obj

	if err := os.MkdirAll(fullDir, 0775); err != nil {
		return err
	}

	// Create a temp file in the same directory to ensure a fast Rename
	tmp, err := os.CreateTemp(s.objDir, "tmp_obj_")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())

	// Use our internal helper to write the compressed data
	if err := s.writeCompressed(tmp, obj); err != nil {
		tmp.Close()
		return err
	}
	tmp.Close()

	return os.Rename(tmp.Name(), fp.Join(fullDir, fileName))
}

func (s *ObjectStorer) writeCompressed(w io.Writer, obj *ResolvedObject) error {
	s.zw.Reset(w)
	s.zw.Write(TypeToBytes(obj.Type))
	s.zw.Write([]byte{' '})
	s.zw.Write(fmt.Appendf(nil, "%d", len(obj.Data)))
	s.zw.Write([]byte{'\x00'})
	s.zw.Write(obj.Data)
	s.zw.Close()
	return s.zw.Close()
}
