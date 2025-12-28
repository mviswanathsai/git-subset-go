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
	"io"
	"io/fs"
	"math/bits"
	"os"
	fp "path/filepath"
	"slices"
	"strings"
	"time"
)

const (
	gitObjDir      = ".git/objects"
	objheaderdelim = 0
	gitExModeOct   = fs.FileMode(0111)
	gitDirMode     = "40000"
	gitRegMode     = "100644"
	gitExMode      = "100755"
	OBJ_COMMIT     = 1
	OBJ_TREE       = 2
	OBJ_BLOB       = 3
	OBJ_TAG        = 4
	OBJ_OFS_DELTA  = 6
	OBJ_REF_DELTA  = 7
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
		filename := fp.Join(gitObjDir, blobdir, blobname)
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
		r.ReadBytes(objheaderdelim)
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

		var tmpw io.Writer
		if write {
			tmpf := createTempObjFile()
			defer os.Remove(tmpf.Name())
			tmpw = tmpf
		} else {
			tmpw = io.Discard
		}

		f, finfo := openFile(filename)

		hash := sha1.New()
		zw := zlib.NewWriter(tmpw)
		mw := io.MultiWriter(hash, zw)
		writeGitObject(mw, "blob", int(finfo.Size()), f)

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
		objDirName, objFileName := decomposeHash(treeh)
		filepath := fp.Join(gitObjDir, objDirName, objFileName)
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

		tmpf := createTempObjFile()
		defer os.Remove(tmpf.Name())

		hash := sha1.New()
		zw := zlib.NewWriter(tmpf)
		mw := io.MultiWriter(hash, zw)

		writeGitObject(mw, "tree", tree.Len(), tree)
		zw.Close()
		tmpf.Close()
		h := hex.EncodeToString(hash.Sum(nil))

		objDirName, objFileName := decomposeHash(h)
		err = os.MkdirAll(fp.Join(gitObjDir, objDirName), 0775)
		if err != nil && !errors.Is(err, fs.ErrExist) {
			fmt.Fprintf(os.Stderr, "Error creating dir: %v\n", err)
			os.Exit(1)
		}

		objFilePath := fp.Join(gitObjDir, objDirName, objFileName)
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

		tmpf := createTempObjFile()
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

		writeGitObject(mw, "commit", b.Len(), b)
		zw.Close()
		tmpf.Close()

		h := hex.EncodeToString(hash.Sum(nil))
		objDirName, objFileName := decomposeHash(h)
		if err := os.MkdirAll(fp.Join(gitObjDir, objDirName), 0775); err != nil && !errors.Is(err, fs.ErrExist) {
			fmt.Fprintf(os.Stderr, "Error creating dir: %v\n", err)
			os.Exit(1)
		}
		objFilePath := fp.Join(gitObjDir, objDirName, objFileName)
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
		divider := "----------"
		for i := 1; ; i++ {
			if uint32(i) > binary.BigEndian.Uint32(nObj) {
				fmt.Printf("Total number of objects is %d\n", binary.BigEndian.Uint32(nObj))
				break
			}

			objType, objSize := readObjHeader(br)

			if objType == 6 {
				// The required negative offet from the type byte
				negOfs := readDeltaOfs(br)
				fmt.Printf("The negative offset for ofs_delta_%d: %d\n", i, negOfs)
			}

			var dstWriter io.Writer
			if objType == 6 {
				dstWriter = os.Stdout
			} else {
				dstWriter = io.Discard
			}

			fmt.Fprintf(dstWriter, "%sBEGIN OBJECT-%d%s\n\n", divider, i, divider)
			fmt.Fprintf(dstWriter, "The size of the object-%d: %d\n", i, objSize)
			fmt.Fprintf(dstWriter, "The type of the object-%d: %s\n", i, objectType(objType))

			zr, _ := zlib.NewReader(br)
			if objType == 6 {
				var buf bytes.Buffer
				// mw := io.MultiWriter(dstWriter, &buf)
				_, err = io.Copy(&buf, zr)
				srcSize, dstSize := readDeltaHeader(&buf)
				fmt.Fprintf(dstWriter, "\n")
				fmt.Fprintf(dstWriter, "The src buffer size:%d\nThe dst buffer size:%d\n", srcSize, dstSize)
				b, _ := (&buf).ReadByte()
				if b&0x80 != 0 {
					fmt.Fprintf(dstWriter, "Copy instruction\n")
					ofs := bits.Reverse8(((b & 0b01111000) >> 3))
					size := bits.Reverse8(b & 0b00000111)
					fmt.Fprintf(dstWriter, "The byte is: %b\n", b)
					fmt.Fprintf(dstWriter, "Size of copy instruction: %d\nOffset of copy instruction:%d\n", size, ofs)
                    // We need to reconstruct deltified objects recursively
				} else {
					fmt.Fprintf(dstWriter, "Insert instruction\n")
				}
				_, err = io.Copy(dstWriter, &buf)
				fmt.Fprintf(dstWriter, "\n")
			} else {
				_, err = io.Copy(dstWriter, zr)
				if err != nil {
					fmt.Fprintf(dstWriter, "An error occurred while decompressing: %v\n", err)
					os.Exit(1)
				}
				fmt.Fprintf(dstWriter, "\n")
			}

			fmt.Fprintf(dstWriter, "%sEND OBJECT-%d%s\n\n", divider, i, divider)
		}

	default:
		fmt.Fprintf(os.Stderr, "Unknown command %s\n", command)
		os.Exit(1)
	}
}

// Copy instruction: offset in the source buffer and the length of data to copy to the destination buffer
// Insert instruction: number of bytes to copy from delta buffer into the target - bytes that are not part of the source buffer
// There seem to be 3 buffers: source, destination and delta

// The delta object header contains the length of the source and the target buffers: 2 VLQ's in the header
// Then directly the payload with copy/insert instructions
type DeltaObject struct {
	instructions []string
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

func readDeltaOfs(br *bufio.Reader) uint64 {
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
	case OBJ_COMMIT:
		return "commit"
	case OBJ_TREE:
		return "tree"
	case OBJ_BLOB:
		return "blob"
	case OBJ_TAG:
		return "tag"
	case OBJ_OFS_DELTA:
		return "ofs_delta"
	case OBJ_REF_DELTA:
		return "ref_delta"
	default:
		return ""
	}
}

func decomposeHash(hash string) (dirName, fileName string) {
	return hash[:2], hash[2:]
}

func createTempObjFile() *os.File {
	tmpf, err := os.CreateTemp(gitObjDir, "tmp_obj_")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating temp file: %v\n", err)
		os.Exit(1)
	}
	return tmpf
}

func openFile(filename string) (*os.File, fs.FileInfo) {
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

func writeGitObject(writer io.Writer, objType string, payloadSize int, payload io.Reader) {
	header := fmt.Sprintf("%s %d\x00", objType, payloadSize)
	if _, err := writer.Write([]byte(header)); err != nil {
		fmt.Fprintf(os.Stderr, "Error writing object: %v\n", err)
	}
	if _, err := io.Copy(writer, payload); err != nil {
		fmt.Fprintf(os.Stderr, "Error writing object: %v\n", err)
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
			tmpf := createTempObjFile()
			defer os.Remove(tmpf.Name())

			sd, err := os.ReadDir(fp.Join(currentPath, e.Name()))
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error walking current working directory: %v\n", err)
			}

			sdTreeBody := generateTreePayload(sd, fp.Join(currentPath, e.Name()))

			hash := sha1.New()
			zw := zlib.NewWriter(tmpf)
			mw := io.MultiWriter(hash, zw)

			writeGitObject(mw, "tree", sdTreeBody.Len(), sdTreeBody)
			tmpf.Close()
			zw.Close()

			hsum := hash.Sum(nil)

			//    objDirName, objFileName := pathFromHash(h)
			//    err = os.MkdirAll(fp.Join(gitObjDir, objDirName), 0775)
			//    if err != nil && !errors.Is(err, fs.ErrExist) {
			//    	fmt.Fprintf(os.Stderr, "Error creating dir: %v\n", err)
			//    	os.Exit(1)
			//    }

			//    objFilePath := fp.Join(gitObjDir, objDirName, objFileName)
			//    os.Rename(tmpf.Name(), objFilePath)

			entry := GitTreeEntry{
				Mode: gitDirMode,
				Name: e.Name(),
				Hash: hsum,
			}
			entries = append(entries, entry)

		} else if !e.IsDir() {
			tmpf := createTempObjFile()
			defer os.Remove(tmpf.Name())

			f, finfo := openFile(fp.Join(currentPath, e.Name()))

			hash := sha1.New()
			zw := zlib.NewWriter(tmpf)
			mw := io.MultiWriter(hash, zw)

			writeGitObject(mw, "blob", int(finfo.Size()), f)
			hsum := hash.Sum(nil)
			zw.Close()
			tmpf.Close()

			////	objDirName, objFileName := pathFromHash(h)
			////	err = os.MkdirAll(fp.Join(gitObjDir, objDirName), 0775)
			////	if err != nil && !errors.Is(err, fs.ErrExist) {
			////		fmt.Fprintf(os.Stderr, "Error creating dir: %v\n", err)
			////		os.Exit(1)
			////	}

			////	objFilePath := fp.Join(gitObjDir, objDirName, objFileName)
			////	if os.Rename(tmpf.Name(), objFilePath) != nil {
			////		fmt.Fprintf(os.Stderr, "Error creating blob object: %v\n", err)
			////		os.Exit(1)
			////	}

			fmode := finfo.Mode().Perm()
			var mode string
			if fmode&gitExModeOct != 0 {
				mode = gitExMode
			} else {
				mode = gitRegMode
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
		if a.Mode == gitDirMode {
			aName += "/"
		}
		if b.Mode == gitDirMode {
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
