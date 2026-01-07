package main

import (
	"bytes"
	"crypto/sha1"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	fp "path/filepath"
	"slices"
	"strings"
	"syscall"

	"github.com/codecrafters-io/git-starter-go/internal/git"
)

type RepoBuilder struct {
	objectSource map[string]*ResolvedObject
	refs         map[string][]byte
}

func (r *RepoBuilder) WriteIndex(indexEntries []*IndexEntry) error {
	slices.SortFunc(indexEntries, func(a, b *IndexEntry) int {
		return strings.Compare(a.Path, b.Path)
	})

	tmpf, err := os.CreateTemp(git.GitDir, "tmp_index_")
	if err != nil {
		return err
	}
	defer os.Remove(tmpf.Name())

	sha := sha1.New()
	mw := io.MultiWriter(tmpf, sha)
	if _, err := mw.Write([]byte{'D', 'I', 'R', 'C'}); err != nil {
		return err
	}
	if err := binary.Write(mw, binary.BigEndian, uint32(2)); err != nil {
		return err
	}
	if err := binary.Write(mw, binary.BigEndian, uint32(len(indexEntries))); err != nil {
		return err
	}
	for _, entry := range indexEntries {
		if err := writeGitIndexLine(entry, mw); err != nil {
			return err
		}
	}
	h := sha.Sum(nil)
	if _, err := tmpf.Write(h); err != nil {
		return err
	}
	return os.Rename(tmpf.Name(), git.GitIndexPath)
}

func (builder *RepoBuilder) CheckoutHeadCommit() (*checkoutResult, error) {
	return builder.CheckoutCommit(builder.refs["HEAD"])
}

func (builder *RepoBuilder) CheckoutCommit(commitSHA []byte) (*checkoutResult, error) {
	result := &checkoutResult{indexEntries: make([]*IndexEntry, 0, 32)}
	headCommit := builder.objectSource[string(commitSHA)]
	treeSHA := returnCommitTreeSHA(headCommit.Data)
	treeData := builder.objectSource[treeSHA].Data
	if err := builder.buildRepository("", treeData, result); err != nil {
		return nil, err
	}
	return result, nil
}

func (builder *RepoBuilder) buildRepository(currentDir string, treeData []byte, result *checkoutResult) error {
	treeEntries := parseTree(treeData)
	for _, treeEntry := range treeEntries {
		if treeEntry.mode != git.GitDirMode {
			// write the files
			filepath := fp.Join(currentDir, treeEntry.name)
			if err := os.WriteFile(filepath, builder.objectSource[treeEntry.sha1].Data, TranslateGitModeToFileMode(treeEntry.mode)); err != nil {
				return err
			}

			fileInfo, err := os.Lstat(filepath)
			if err != nil {
				return err
			}

			shaBytes, err := hex.DecodeString(treeEntry.sha1)
			if err != nil {
				return err
			}

			stat := fileInfo.Sys().(*syscall.Stat_t)
			lengthFlag := min(len(filepath), 0x0FFF)
			result.indexEntries = append(result.indexEntries, &IndexEntry{
				CtimeSec:  uint32(stat.Ctim.Sec),
				CtimeNano: uint32(stat.Ctim.Nsec),
				MtimeSec:  uint32(stat.Mtim.Sec),
				MtimeNano: uint32(stat.Mtim.Nsec),
				Dev:       uint32(stat.Dev),
				Ino:       uint32(stat.Ino),
				Mode:      uint32(TranslateGitModeToUint(treeEntry.mode)),
				UID:       uint32(stat.Uid),
				GID:       uint32(stat.Gid),
				Size:      uint32(fileInfo.Size()),
				SHA:       [20]byte(shaBytes),
				Flags:     uint16(lengthFlag),
				Path:      filepath,
			})
		} else {
			currentDir = fp.Join(currentDir, treeEntry.name)
			if err := os.MkdirAll(currentDir, 0755); err != nil {
				return err
			}
			treeData := builder.objectSource[treeEntry.sha1].Data
			if err := builder.buildRepository(currentDir, treeData, result); err != nil {
				return err
			}
		}
	}
	return nil
}

func (builder *RepoBuilder) CreateHeads() error {
	originHeadPath := fp.Join(".git", "refs/remotes/origin/HEAD")
	remoteHeadSHA, ok := builder.refs["HEAD"]
	if !ok {
		return fmt.Errorf("HEAD ref not in list of refs")
	}

	var localHeadRef string
	for ref, SHA := range builder.refs {
		if ref != "HEAD" && slices.Equal(SHA, remoteHeadSHA) {
			localHeadRef = ref
			break
		}
	}

	if localHeadRef == "" {
		return fmt.Errorf("No refs match the HEAD SHA")
	}

	// Set the remote HEAD ref
	localRef := strings.Replace(localHeadRef, "refs/heads/", "refs/remotes/origin/", 1)
	symbolicContent := fmt.Sprintf("ref: %s\n", localRef)
	if err := os.WriteFile(originHeadPath, []byte(symbolicContent), 0644); err != nil {
		return err
	}

	// Create the LOCAL branch ref (e.g., .git/refs/heads/main)
	localBranchPath := fp.Join(".git", localHeadRef)
	if err := os.MkdirAll(fp.Dir(localBranchPath), 0755); err != nil {
		return err
	}
	if err := os.WriteFile(localBranchPath, append(remoteHeadSHA, '\n'), 0644); err != nil {
		return err
	}
	rootHeadPath := fp.Join(".git", "HEAD")
	rootHeadContent := fmt.Sprintf("ref: %s\n", localHeadRef)
	return os.WriteFile(rootHeadPath, []byte(rootHeadContent), 0644)
}

func (builder *RepoBuilder) CreateRefs() error {
	for ref, SHA := range builder.refs {
		localRef := strings.Replace(ref, "refs/heads/", "refs/remotes/origin/", 1)

		destPath := fp.Join(".git", localRef)
		parentDir := fp.Dir(destPath)

		if err := os.MkdirAll(parentDir, 0755); err != nil {
			return err
		}

		return os.WriteFile(destPath, append(SHA, '\n'), 0644)
	}
	return nil
}

type IndexEntry struct {
	CtimeSec  uint32
	CtimeNano uint32
	MtimeSec  uint32
	MtimeNano uint32
	Dev       uint32
	Ino       uint32
	Mode      uint32
	UID       uint32
	GID       uint32
	Size      uint32
	SHA       [20]byte // Binary SHA-1 (not hex)
	Flags     uint16   // 1-bit assume-unchanged, 1-bit extended, 2-bit stage, 12-bit name length
	Path      string   // The relative path (e.g., "dir/file.txt")
}

func writeGitIndexLine(e *IndexEntry, w io.Writer) error {
	var buf [62]byte
	binary.BigEndian.PutUint32(buf[0:4], e.CtimeSec)
	binary.BigEndian.PutUint32(buf[4:8], e.CtimeNano)
	binary.BigEndian.PutUint32(buf[8:12], e.MtimeSec)
	binary.BigEndian.PutUint32(buf[12:16], e.MtimeNano)
	binary.BigEndian.PutUint32(buf[16:20], e.Dev)
	binary.BigEndian.PutUint32(buf[20:24], e.Ino)
	binary.BigEndian.PutUint32(buf[24:28], e.Mode)
	binary.BigEndian.PutUint32(buf[28:32], e.UID)
	binary.BigEndian.PutUint32(buf[32:36], e.GID)
	binary.BigEndian.PutUint32(buf[36:40], e.Size)
	copy(buf[40:60], e.SHA[:])
	binary.BigEndian.PutUint16(buf[60:62], e.Flags)

	if _, err := w.Write(buf[:]); err != nil {
		return err
	}
	if _, err := w.Write([]byte(e.Path)); err != nil {
		return err
	}

	// Padding logic: Git requires 1-8 null bytes to reach
	// a total entry length that is a multiple of 8.
	entryLenSoFar := 62 + len(e.Path)
	padding := 8 - (entryLenSoFar % 8)
	if padding == 0 {
		padding = 8
	}
	if _, err := w.Write(make([]byte, padding)); err != nil {
		return err
	}
	return nil
}

type TreeEntry struct {
	mode string
	name string
	sha1 string
}

func parseTree(treeData []byte) []*TreeEntry {
	var out []*TreeEntry
	for {
		spaceIdx := bytes.IndexByte(treeData, ' ')
		delimIdx := bytes.IndexByte(treeData, '\x00')

		if spaceIdx == -1 || delimIdx == -1 {
			fmt.Fprintf(os.Stderr, "Unexpected absence of delim byte")
		}

		modei := string(treeData[:spaceIdx])
		namei := string(treeData[spaceIdx+1 : delimIdx])
		hashi := hex.EncodeToString(treeData[delimIdx+1 : delimIdx+21])

		out = append(out, &TreeEntry{mode: modei, name: namei, sha1: hashi})

		if isLastTreeEntry(treeData, delimIdx) {
			break
		}

		treeData = treeData[delimIdx+21:]
	}
	return out
}

func TranslateGitModeToUint(gitMode string) uint32 {
	switch gitMode {
	case "100644":
		return 0o100644
	case "100755":
		return 0o100755
	case "40000", "040000":
		return 0o040000
	case "120000":
		return 0o120000
	default:
		return 0o100644
	}
}

// TranslateGitMode converts Git mode strings to standard Unix os.FileMode
func TranslateGitModeToFileMode(gitMode string) os.FileMode {
	switch gitMode {
	case "100755":
		return 0755
	case "100644":
		return 0644
	case "40000", "040000":
		return 0755
	case "120000":
		return os.ModeSymlink
	default:
		return 0644
	}
}
