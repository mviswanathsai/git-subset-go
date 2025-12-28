package main

import (
	"bufio"
	"bytes"
	"compress/zlib"
	"crypto/sha1"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	files "github.com/codecrafters-io/git-starter-go/internal/files"
	git "github.com/codecrafters-io/git-starter-go/internal/git"
	hashes "github.com/codecrafters-io/git-starter-go/internal/hashes"
	"io"
	"io/fs"
	"os"
	fp "path/filepath"
	"slices"
	"strings"
	"time"
)

// Usage: your_program.sh <command> <arg1> <arg2> ...
func main() {
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
		filename := fp.Join(git.GitObjDir, blobdir, blobname)
		f, err := os.Open(filename)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error opening file: %v\n", err)
			os.Exit(1)
		}

		zr, err := zlib.NewReader(f)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error decompressing file: %v\n", err)
			os.Exit(1)
		}
		defer zr.Close()

		r := bufio.NewReader(zr)
		r.ReadBytes(git.ObjHeaderDelim)
		io.Copy(os.Stdout, r)

	case "hash-object":
		var filename string
		var write bool

		if len(os.Args) == 3 {
			filename = os.Args[2]
		} else if len(os.Args) == 4 {
			if os.Args[2] == "-w" {
				filename = os.Args[3]
				write = true
			} else {
				fmt.Fprintf(os.Stderr, "Unknown flag %s for command hash-object\n", os.Args[2])
				os.Exit(1)
			}
		}

		f, finfo := files.OpenFile(filename)
		h := hashes.HashObject(f, finfo.Size(), "blob", write)

		// Print the hex encoding of the hash.
		fmt.Print(h)

	case "ls-tree":
		var treeh string
		var nameonly bool
		if len(os.Args) == 4 {
			if os.Args[2] != "--name-only" {
				fmt.Fprintf(os.Stderr, "Unknown argument %s for command ls-tree\n", os.Args[2])
				os.Exit(1)
			}
			treeh = os.Args[3]
			nameonly = true
		} else {
			treeh = os.Args[2]
		}

		// given a hash, read the file and output
		objDirName, objFileName := hashes.DecomposeHash(treeh)
		filepath := fp.Join(git.GitObjDir, objDirName, objFileName)
		f, err := os.Open(filepath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error opening file: %v\n", err)
			os.Exit(1)
		}

		zr, err := zlib.NewReader(f)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error reading file: %v\n", err)
			os.Exit(1)
		}

		b, err := io.ReadAll(zr)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error reading file: %v\n", err)
			os.Exit(1)
		}

		// Trim the tree header
		delimIdx := bytes.IndexByte(b, '\x00')
		b = b[delimIdx+1:]

		out := parseTreeObject(b, nameonly)
		slices.Sort(out)
		fmt.Printf("%v\n", strings.Join(out, "\n"))

	case "write-tree":
		// Get the current working directory
		ex, err := os.Getwd()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error getting current working dir: %v\n", err)
		}

		cwd, err := os.ReadDir(ex)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error getting current working dir: %v\n", err)
		}

		tree := generateTreePayload(cwd, ".")

		tmpf := files.CreateTempObjFile()
		defer os.Remove(tmpf.Name())

		hash := sha1.New()
		zw := zlib.NewWriter(tmpf)
		mw := io.MultiWriter(hash, zw)

		files.WriteGitObject(mw, "tree", tree.Len(), tree)
		zw.Close()
		tmpf.Close()
		h := hex.EncodeToString(hash.Sum(nil))

		objDirName, objFileName := hashes.DecomposeHash(h)
		err = os.MkdirAll(fp.Join(git.GitObjDir, objDirName), 0775)
		if err != nil && !errors.Is(err, fs.ErrExist) {
			fmt.Fprintf(os.Stderr, "Error creating dir: %v\n", err)
			os.Exit(1)
		}

		objFilePath := fp.Join(git.GitObjDir, objDirName, objFileName)
		if os.Rename(tmpf.Name(), objFilePath) != nil {
			fmt.Fprintf(os.Stderr, "Error creating blob object: %v\n", err)
			os.Exit(1)
		}

		fmt.Println(h)

	case "commit-tree":
		treesha := os.Args[2]
		commitsha := os.Args[4]
		message := os.Args[6]
		author := "Teenus Lorvalds"
		email := "teenus@lorvalds.com"

		tmpf := files.CreateTempObjFile()
		defer tmpf.Close()

		t := time.Now()
		timestamp := t.Unix()
		offset := t.Format("-0700")

		var sb strings.Builder
		fmt.Fprintf(&sb, "tree %s\n", treesha)
		fmt.Fprintf(&sb, "parent %s\n", commitsha)
		fmt.Fprintf(&sb, "author %s <%s> %d %s\n", author, email, timestamp, offset)
		fmt.Fprintf(&sb, "\n%s\n", message)

		b := bytes.NewBuffer([]byte(sb.String()))
		hash := sha1.New()
		zw := zlib.NewWriter(tmpf)
		mw := io.MultiWriter(hash, zw)

		files.WriteGitObject(mw, "commit", b.Len(), b)
		zw.Close()
		tmpf.Close()

		h := hex.EncodeToString(hash.Sum(nil))
		objDirName, objFileName := hashes.DecomposeHash(h)
		if err := os.MkdirAll(fp.Join(git.GitObjDir, objDirName), 0775); err != nil && !errors.Is(err, fs.ErrExist) {
			fmt.Fprintf(os.Stderr, "Error creating dir: %v\n", err)
			os.Exit(1)
		}
		objFilePath := fp.Join(git.GitObjDir, objDirName, objFileName)
		if err := os.Rename(tmpf.Name(), objFilePath); err != nil {
			fmt.Fprintf(os.Stderr, "Error creating commit object: %v\n", err)
			os.Exit(1)
		}

		fmt.Println(h)

	case "parse-packfile":
		pfPath := os.Args[2]
		// Open the file instead for comparison
		pf, err := os.Open(pfPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error opening pack file: %v", err)
			os.Exit(1)
		}

		br := bufio.NewReader(pf)

		ver := make([]byte, 4)
		nObj := make([]byte, 4)

		// Discard the first 4 bytes
		br.Discard(4)
		// Read the version and the nobjects
		io.ReadFull(br, ver)
		io.ReadFull(br, nObj)
		packIndex := make(map[int64]indexEntry)
		for i := 1; ; i++ {
			if uint32(i) > binary.BigEndian.Uint32(nObj) {
				fmt.Printf("Total number of objects is %d\n", binary.BigEndian.Uint32(nObj))
				break
			}

			dstWriter := os.Stdout
			headerOfs := currentOffset(pf, br)
			objType, objSize := readObjHeader(br)
			dataOfs := currentOffset(pf, br)

			packIndex[headerOfs] = newIndexEntry(objType, objSize, uint8(dataOfs-headerOfs))

			if objType == 6 {
				// The required negative offet from the type byte
				negOfs := readDeltaNegOfs(br)
				//fmt.Fprintf(dstWriter, "The negative offset for ofs_delta_%d: %d\n", i, negOfs)
				fmt.Fprintf(dstWriter, "The parent offset for ofs_delta_%d: %d\n", i, uint64(headerOfs)-negOfs)
			}

			zr, _ := zlib.NewReader(br)
			if objType == 6 {
				var buf bytes.Buffer
				parseDeltaObj(&buf, zr, dstWriter)
			} else {
				h := hashes.HashObject(zr, int64(objSize), objectType(objType), false)
				fmt.Fprintf(dstWriter, "%s %s %d %d\n", h, objectType(objType), objSize, headerOfs)
			}
		}

	default:
		fmt.Fprintf(os.Stderr, "Unknown command %s\n", command)
		os.Exit(1)
	}
}

func currentOffset(f *os.File, br *bufio.Reader) int64 {
	fofs, _ := f.Seek(0, 1)
	currOfs := fofs - int64(br.Buffered())

	return currOfs
}

type indexEntry struct {
	objType    uint8
	objSize    uint64
	dataOffset uint8 // The offset from the headerOfs to find the data, can be a maximum of 64 bits -> 8 bytes -> can be stored in uint8
}

type deltaObj struct {
	headerOfs  int64
	srcBufSize uint64
	dstBufSize uint64
	negOfs     int64
	ops        []DeltaOps
}

type DeltaOps interface {
	kind() byte
}

type OpCodeCopy struct {
	offset uint64
	size   uint64
}

func (OpCodeCopy) Kind() byte {
	return 1
}

type OpCodeInsert struct {
	payloadSize uint8
	payload     []byte
}

func (OpCodeInsert) Kind() byte {
	return 0
}

func newIndexEntry(objType uint8, objSize uint64, dataOffset uint8) indexEntry {
	return indexEntry{
		objType:    objType,
		objSize:    objSize,
		dataOffset: dataOffset,
	}
}

func parseDeltaObj(buf *bytes.Buffer, zr io.ReadCloser, dstWriter io.Writer) {
	// mw := io.MultiWriter(dstWriter, &buf)
	io.Copy(buf, zr)
	srcSize, dstSize := readDeltaHeader(buf)
	fmt.Fprintf(dstWriter, "\n")
	fmt.Fprintf(dstWriter, "src buffer size:%d\ndst buffer size:%d\n", srcSize, dstSize)
	fmt.Fprintf(dstWriter, "\n")

	for buf.Len() != 0 {
		b, _ := (buf).ReadByte()
		if b&0x80 != 0 {
			fmt.Fprintf(dstWriter, "Copy instruction\n")
			copyOfsFlags := (b & git.CopyOffsetFlagsMask)
			copySizeFlags := (b & git.CopySizeFlagsMask) >> git.CopySizeFlagsShift
			ofs := readDeltaCopyOffset(copyOfsFlags, buf)
			size := readDeltaCopySize(copySizeFlags, buf)
			fmt.Fprintf(dstWriter, "The copy header byte is: %b\n", b)
			fmt.Fprintf(dstWriter, "The copy size is: %d\n", size)
			fmt.Fprintf(dstWriter, "The copy offset is: %d\n", ofs)
			fmt.Fprintf(dstWriter, "\n")
			// We need to reconstruct deltified objects recursively
		} else {
			fmt.Fprintf(dstWriter, "Insert instruction\n")
			insertSize := (b & git.InsertSizeMask)
			fmt.Fprintf(dstWriter, "The insert header byte is: %b\n", b)
			fmt.Fprintf(dstWriter, "The insert size is: %d\n", insertSize)
			insertPayloadBuf := make([]byte, insertSize)
			io.ReadFull(buf, insertPayloadBuf)
			fmt.Fprintf(dstWriter, "The insert payload is: %s\n", insertPayloadBuf)
			fmt.Fprintf(dstWriter, "\n")
		}
	}
	io.Copy(dstWriter, buf)
	fmt.Fprintf(dstWriter, "\n")
}

func readObjHeader(br *bufio.Reader) (byte, uint64) {
	var i int
	var objSize uint64
	var objType byte
	for {
		b, _ := br.ReadByte()
		if i == 0 {
			objSize = uint64(b & 0b00001111)
			objType = (b & 0b01110000) >> 4
		} else {
			objSize = uint64(b&0b01111111)<<(4+(i-1)*7) | uint64(objSize)
		}
		if b&0b10000000>>7 == 0 {
			break
		}
		i++
	}
	return objType, objSize
}

// There are two sizes to read
func readDeltaHeader(buf *bytes.Buffer) (srcSize uint64, dstSize uint64) {
	srcSize = readDeltaSize(buf)
	dstSize = readDeltaSize(buf)
	return srcSize, dstSize
}

func readDeltaSize(buf *bytes.Buffer) uint64 {
	var i uint64
	var size uint64
	i++
	for {
		b, _ := buf.ReadByte()
		size = uint64(b&0b01111111)<<((i-1)*7) | size
		if b&0x80 == 0 {
			break
		}
		i++
	}
	return size
}

func readDeltaCopyOffset(ofsFlags byte, br *bytes.Buffer) (ofs uint64) {
	for i := 0; i < git.CopyOffsetFlagsLen; i++ {
		if (0b00000001 & (ofsFlags >> i)) == 1 {
			b, _ := br.ReadByte()
			ofs |= uint64(b) << (8 * i)
		}
	}
	return ofs
}

func readDeltaCopySize(sizeFlags byte, br *bytes.Buffer) (size uint64) {
	for i := 0; i < git.CopySizeFlagsLen; i++ {
		if (0b00000001 & (sizeFlags >> i)) == 1 {
			b, _ := br.ReadByte()
			size |= uint64(b) << (8 * i)
		}
	}
	if size == 0 {
		return git.CopySizeZero
	}
	return size
}

func readDeltaNegOfs(br *bufio.Reader) uint64 {
	var i uint64
	var size uint64

	for {
		if i != 0 {
			// The +1 rule
			size++
		}
		b, _ := br.ReadByte()
		size = size<<7 | uint64(b&0b01111111)
		if b&0x80 == 0 {
			break
		}
		i++
	}
	return size
}

func objectType(input byte) string {
	switch input {
	case git.OBJ_COMMIT:
		return git.GIT_COMMIT
	case git.OBJ_TREE:
		return git.GIT_TREE
	case git.OBJ_BLOB:
		return git.GIT_BLOB
	case git.OBJ_TAG:
		return git.GIT_TAG
	case git.OBJ_OFS_DELTA:
		return git.GIT_OFS_DELTA
	case git.OBJ_REF_DELTA:
		return git.GIT_REF_DELTA
	default:
		return ""
	}
}

func parseTreeObject(treeObject []byte, nameonly bool) []string {
	var out []string
	for {
		spaceIdx := bytes.IndexByte(treeObject, ' ')
		delimIdx := bytes.IndexByte(treeObject, '\x00')

		if spaceIdx == -1 || delimIdx == -1 {
			fmt.Fprintf(os.Stderr, "Unexpected absence of delim byte")
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
	return out
}

type GitTreeEntry struct {
	Name string
	Mode string
	Hash []byte
}

func (e GitTreeEntry) String() string {
	return fmt.Sprintf("%s %s\x00%s", e.Mode, e.Name, e.Hash)
}

// TODO: maybe use goroutines here for performance.
func generateTreePayload(cwd []fs.DirEntry, currentPath string) *bytes.Buffer {
	var entries []GitTreeEntry
	for _, e := range cwd {
		if e.Name() == ".git" {
			continue
		}
		if e.IsDir() {
			tmpf := files.CreateTempObjFile()
			defer os.Remove(tmpf.Name())

			sd, err := os.ReadDir(fp.Join(currentPath, e.Name()))
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error walking current working directory: %v\n", err)
			}

			sdTreeBody := generateTreePayload(sd, fp.Join(currentPath, e.Name()))

			hash := sha1.New()
			zw := zlib.NewWriter(tmpf)
			mw := io.MultiWriter(hash, zw)

			files.WriteGitObject(mw, "tree", sdTreeBody.Len(), sdTreeBody)
			tmpf.Close()
			zw.Close()

			hsum := hash.Sum(nil)

			//    objDirName, objFileName := pathFromHash(h)
			//    err = os.MkdirAll(fp.Join(git.GitObjDir, objDirName), 0775)
			//    if err != nil && !errors.Is(err, fs.ErrExist) {
			//    	fmt.Fprintf(os.Stderr, "Error creating dir: %v\n", err)
			//    	os.Exit(1)
			//    }

			//    objFilePath := fp.Join(git.GitObjDir, objDirName, objFileName)
			//    os.Rename(tmpf.Name(), objFilePath)

			entry := GitTreeEntry{
				Mode: git.GitDirMode,
				Name: e.Name(),
				Hash: hsum,
			}
			entries = append(entries, entry)

		} else if !e.IsDir() {
			tmpf := files.CreateTempObjFile()
			defer os.Remove(tmpf.Name())

			f, finfo := files.OpenFile(fp.Join(currentPath, e.Name()))

			hash := sha1.New()
			zw := zlib.NewWriter(tmpf)
			mw := io.MultiWriter(hash, zw)

			files.WriteGitObject(mw, "blob", int(finfo.Size()), f)
			hsum := hash.Sum(nil)
			zw.Close()
			tmpf.Close()

			////	objDirName, objFileName := pathFromHash(h)
			////	err = os.MkdirAll(fp.Join(git.GitObjDir, objDirName), 0775)
			////	if err != nil && !errors.Is(err, fs.ErrExist) {
			////		fmt.Fprintf(os.Stderr, "Error creating dir: %v\n", err)
			////		os.Exit(1)
			////	}

			////	objFilePath := fp.Join(git.GitObjDir, objDirName, objFileName)
			////	if os.Rename(tmpf.Name(), objFilePath) != nil {
			////		fmt.Fprintf(os.Stderr, "Error creating blob object: %v\n", err)
			////		os.Exit(1)
			////	}

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

	var treeBody string
	for _, entry := range entries {
		treeBody += entry.String()
	}

	return bytes.NewBuffer([]byte(treeBody))
}
