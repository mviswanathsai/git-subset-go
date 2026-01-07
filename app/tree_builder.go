package main

import (
	"bytes"
	"compress/zlib"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"os"
	fp "path/filepath"
	"slices"
	"strings"

	"github.com/codecrafters-io/git-starter-go/internal/files"
	"github.com/codecrafters-io/git-starter-go/internal/git"
	"github.com/codecrafters-io/git-starter-go/internal/hashes"
)

type TreeBuilder struct {
	zw   *zlib.Writer
	hash hash.Hash
}

// TODO: maybe use goroutines here for performance.
func (b *TreeBuilder) generateTreePayload(cwd []fs.DirEntry, currentPath string) (*bytes.Buffer, error) {
	var entries []GitTreeEntry
	for _, e := range cwd {
		if e.Name() == ".git" {
			continue
		} else if e.IsDir() {
			sd, err := os.ReadDir(fp.Join(currentPath, e.Name()))
			if err != nil {
				return nil, fmt.Errorf("Error walking current working directory: %v\n", err)
			}

			subtreePayload, err := b.generateTreePayload(sd, fp.Join(currentPath, e.Name()))
			if err != nil {
				return nil, err
			}

			hsum, err := b.writeAndGetHash("tree", subtreePayload.Len(), subtreePayload)
			if err != nil {
				return nil, fmt.Errorf("Error writing and hashing object data: %v", err)
			}

			entry := GitTreeEntry{
				Mode: git.GitDirMode,
				Name: e.Name(),
				Hash: hsum,
			}
			entries = append(entries, entry)

		} else if !e.IsDir() {

			f, finfo, err := files.OpenFile(fp.Join(currentPath, e.Name()))
			if err != nil {
				return nil, fmt.Errorf("Error opening file: %v", err)
			}

			hsum, err := b.writeAndGetHash("blob", int(finfo.Size()), f)
			if err != nil {
				return nil, fmt.Errorf("Error writing and hashing object data: %v", err)
			}

			fmode := finfo.Mode().Perm()
			var mode string
			if fmode&git.GitExModeOct != 0 {
				mode = git.GitExMode
			} else {
				mode = git.GitRegMode
			}

			treeEntry := GitTreeEntry{
				Mode: mode,
				Name: e.Name(),
				Hash: hsum,
			}
			entries = append(entries, treeEntry)
		}
	}

	slices.SortFunc(entries, func(a, b GitTreeEntry) int {
		aName := a.Name
		bName := b.Name
		if a.Mode == git.GitDirMode {
			aName += "/"
		}
		if b.Mode == git.GitDirMode {
			bName += "/"
		}
		return strings.Compare(aName, bName)
	})

	buf := bytes.NewBuffer(make([]byte, 0, 4096))
	for _, e := range entries {
		// fmt.Sprintf("%s %s\x00%s", e.Mode, e.Name, e.Hash)
		buf.WriteString(e.Mode)
		buf.WriteByte(' ')
		buf.WriteString(e.Name)
		buf.WriteByte('\x00')
		buf.Write(e.Hash)
	}

	return buf, nil
}

func (b *TreeBuilder) writeAndGetHash(objectType string, payloadSize int, payload io.Reader) ([]byte, error) {
	tmpf, err := files.CreateTempObjFile()
	if err != nil {
		return nil, fmt.Errorf("Error writing object to disk: %v", err)
	}
	b.hash.Reset()
	b.zw.Reset(tmpf)
	mw := io.MultiWriter(b.hash, b.zw)
	if err := files.WriteGitObject(mw, objectType, payloadSize, payload); err != nil {
		return nil, fmt.Errorf("Error writing object to disk: %v", err)
	}
	if err := b.zw.Close(); err != nil {
		return nil, err
	}
	tmpf.Close()

	hsum := b.hash.Sum(nil)
	h := hex.EncodeToString(hsum)
	objDirName, objFileName := hashes.DecomposeHash(h)
	if err := os.MkdirAll(fp.Join(git.GitObjDir, objDirName), 0775); err != nil && !errors.Is(err, fs.ErrExist) {
		return nil, err
	}

	objFilePath := fp.Join(git.GitObjDir, objDirName, objFileName)
	if err := os.Rename(tmpf.Name(), objFilePath); err != nil {
		return nil, err
	}
	return hsum, nil
}

func parseTreeObject(treeObject []byte, nameonly bool) ([]string, error) {
	var out []string
	for {
		spaceIdx := bytes.IndexByte(treeObject, ' ')
		delimIdx := bytes.IndexByte(treeObject, '\x00')

		if spaceIdx == -1 || delimIdx == -1 {
			return nil, fmt.Errorf("Unexpected absence of delim byte")
		}

		modei := string(treeObject[:spaceIdx])
		namei := string(treeObject[spaceIdx+1 : delimIdx])
		hashi := hex.EncodeToString(treeObject[delimIdx+1 : delimIdx+21])

		if len(modei) == 5 {
			modei = "0" + modei
		}

		if nameonly {
			out = append(out, namei)
		} else {
			out = append(out, fmt.Sprintf("%s %s %s", modei, hashi, namei))
		}

		if len(treeObject[delimIdx+1:]) == 20 {
			break
		}

		treeObject = treeObject[delimIdx+21:]
	}
	return out, nil
}

type GitTreeEntry struct {
	Name string
	Mode string
	Hash []byte
}

func (e GitTreeEntry) String() string {
	return fmt.Sprintf("%s %s\x00%s", e.Mode, e.Name, e.Hash)
}

func isLastTreeEntry(treeData []byte, delimIdx int) bool {
	return len(treeData[delimIdx+1:]) <= 20
}
