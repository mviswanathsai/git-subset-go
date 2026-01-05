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
	"hash"
	"io"
	"io/fs"
	"net/http"
	"os"
	fp "path/filepath"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	files "github.com/codecrafters-io/git-starter-go/internal/files"
	git "github.com/codecrafters-io/git-starter-go/internal/git"
	hashes "github.com/codecrafters-io/git-starter-go/internal/hashes"
)

const (
	OP_CODE_COPY   = 1
	OP_COPE_INSERT = 0
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
		ex, err := os.Getwd()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error getting current working dir: %v\n", err)
		}

		cwd, err := os.ReadDir(ex)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error getting current working dir: %v\n", err)
		}

		hash := sha1.New()
		zw := zlib.NewWriter(nil)
		defer zw.Close()

		treeBuilder := &TreeBuilder{
			hash: hash,
			zw:   zw,
		}

		payload := treeBuilder.generateTreePayload(cwd, ".")

		tmpf := files.CreateTempObjFile()
		defer os.Remove(tmpf.Name())

		hash.Reset()
		zw.Reset(tmpf)
		mw := io.MultiWriter(hash, zw)

		files.WriteGitObject(mw, "tree", payload.Len(), payload)
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
		t := time.Now()
		timestamp := t.Unix()
		offset := t.Format("-0700")

		buf := new(bytes.Buffer)
		fmt.Fprintf(buf, "tree %s\n", treesha)
		fmt.Fprintf(buf, "parent %s\n", commitsha)
		fmt.Fprintf(buf, "author %s <%s> %d %s\n", author, email, timestamp, offset)
		fmt.Fprintf(buf, "\n%s\n", message)

		tmpf := files.CreateTempObjFile()
		hash := sha1.New()
		zw := zlib.NewWriter(tmpf)
		mw := io.MultiWriter(hash, zw)

		files.WriteGitObject(mw, "commit", buf.Len(), buf)
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

	case "verify-pack":
		pfPath := os.Args[2]
		// Open the file instead for comparison
		pf, fileInfo := files.OpenFile(pfPath)

		h := sha1.New()

		if err := verifyPackTrailer(pf, fileInfo, h); err != nil {
			fmt.Fprintf(os.Stderr, "Invalid Pack: %v", err)
			os.Exit(1)
		}

		br := bufio.NewReader(pf)
		_, objCount, err := readPackHeader(br)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Pack header invalid: %v", err)
			os.Exit(1)
		}

		streamer := &PackStreamer{
			f:   pf,
			br:  br,
			zr:  nil,
			zbr: nil,
		}
		packOrder, packIndex := streamer.BuildPackIndex(objCount)

		builder := &objectBuilder{
			packIndex:   packIndex,
			lookupCache: make(map[uint64]*ResolvedObject),
			packOrder:   packOrder,
			br:          br,
			file:        pf,
			fileInfo:    fileInfo,
			zr:          nil,
			hasher:      h,
		}

		stats := &PackStats{
			ChainCounts:   make(map[int]int),
			NonDeltaCount: 0,
		}

		builder.ForEachObject(stats.ProcessResult)

		stats.PrintSummary()

		fmt.Printf("%s: ok\n", pfPath)

	case "ls-remote":
		// just get the references from a remote repo
		repo := os.Args[2]
		str := fmt.Sprintf("%s/info/refs?service=git-upload-pack", repo)
		resp, err := http.Get(str)
		if err != nil {
			panic(err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNotModified {
			os.Exit(1)
		}

		br := bufio.NewReader(resp.Body)
		if err := validateUploadPackResponse(br); err != nil {
			os.Exit(1)
		}

		readPktLine(br)
		for {
			line, err := readPktLine(br)
			if err != nil || line == nil {
				break
			}
			// If a pktline contains a nul byte, it must be the first ref. Everything after the
			// NUL byte is just capability declarations.
			if i := bytes.IndexByte(line, '\x00'); i != -1 {
				line = line[:i]
			}
			fmt.Printf("%s\t%s\n", line[:40], line[41:])
		}

	case "clone":
		// just get the references from a remote repo
		repo := os.Args[2]
		workingDir := os.Args[3]
		str := fmt.Sprintf("%s/info/refs?service=git-upload-pack", repo)
		resp, err := http.Get(str)
		if err != nil {
			panic(err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNotModified {
			os.Exit(1)
		}

		br := bufio.NewReader(resp.Body)
		firstFive, _ := br.Peek(5)
		if matched, _ := regexp.Match("^[0-9a-f]", firstFive); !matched {
			os.Exit(1)
		}

		// Read till the first flush packet
		svcName, _ := readPktLine(br)
		if !bytes.Equal(svcName, []byte("# service=git-upload-pack")) {
			os.Exit(1)
		}
		readPktLine(br)

		var negotiated []byte
		refs := make(map[string][]byte)
		peeledRefs := make(map[string][]byte)
		for {
			line, err := readPktLine(br)
			if err != nil || line == nil {
				break
			}
			line, caps, found := bytes.Cut(line, []byte{'\x00'})
			if found {
				var buf bytes.Buffer
				for sCap := range strings.SplitSeq(string(caps), " ") {
					if _, ok := git.C_CAPS[sCap]; ok {
						buf.WriteString(sCap)
						buf.WriteByte(' ')
					}
				}
				negotiated = bytes.TrimSpace(buf.Bytes())
			}
			// TODO: maybe deduplicate this in the future
			if bytes.Contains(line, []byte("^{}")) {
				peeledRefs[string(line[41:])] = line[:40]
				continue
			}
			refs[string(line[41:])] = line[:40]
		}
		// Now we have to create the want/have request. In our case, just wants.
		var reqBody bytes.Buffer
		prepareReq(&reqBody, refs, negotiated)
		str2 := fmt.Sprintf("%s/git-upload-pack", repo)
		res, err := http.Post(str2, "application/x-git-upload-pack-request", &reqBody)
		if res.StatusCode != http.StatusOK {
			fmt.Printf("Some yeeyeeass error bro: %d", res.StatusCode)
		}

		// Reset the reader
		br.Reset(res.Body)
		tmp, _ := os.CreateTemp(".", "tmp_pack_")
		defer os.Remove(tmp.Name())
		// Demux the response
		for {
			// 1. Read the 4-byte hex length
			hexLen := make([]byte, 4)
			_, err := io.ReadFull(br, hexLen)
			if err == io.EOF {
				break
			}

			var length int
			fmt.Sscanf(string(hexLen), "%04x", &length)

			if length == 0 { // Flush packet
				continue
			}

			if peek, _ := br.Peek(4); bytes.HasPrefix(peek, []byte("NAK")) {
				br.Discard(length - 4)
				continue
			}
			// 3. Check the first byte (The Channel)
			channel, _ := br.ReadByte()

			switch channel {
			case 1:
				// stream the response body into packfile parsing
				io.CopyN(tmp, br, int64(length-5))
			case 2:
				// This is progress text. Print to stderr.
				io.CopyN(os.Stderr, br, int64(length-5))
			case 3:
				// This is a remote error.
				io.CopyN(os.Stderr, br, int64(length-5))
			}
		}
		// create the .git directory and packfile
		tmp.Seek(-20, 2)
		br.Reset(tmp)
		fileHash := make([]byte, 20)
		io.ReadFull(br, fileHash)
		fileInfo, _ := os.Stat(tmp.Name())

		if err := verifyPackTrailer(tmp, fileInfo, sha1.New()); err != nil {
			fmt.Printf("%s: ok\n", tmp.Name())
		} else {
			fmt.Printf("%s: error\n", tmp.Name())
		}

		err = os.MkdirAll(fp.Join(workingDir, git.GitObjDir, "pack"), 0775)
		if err != nil && !errors.Is(err, fs.ErrExist) {
			fmt.Fprintf(os.Stderr, "Error creating dir: %v\n", err)
			os.Exit(1)
		}

		packFile := fp.Join(workingDir, git.GitObjDir, "pack", fmt.Sprintf("pack-%x.pack", fileHash))
		if err := os.Rename(tmp.Name(), packFile); err != nil {
			fmt.Printf("Error renaming file: %v\n", err)
		}

		os.Chdir(workingDir)
		// Write the objects into the git object store
		pf, fileInfo := files.OpenFile(fp.Join(git.GitObjDir, "pack", fmt.Sprintf("pack-%x.pack", fileHash)))

		br.Reset(pf)
		_, nObj, err := readPackHeader(br)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Pack header invalid: %v", err)
			os.Exit(1)
		}

		var zr io.ReadCloser

		builder := &copyBuilder{
			packIndex:   make(map[uint64]PackNode),
			lookupCache: make(map[uint64]*ResolvedObject),
			packOrder:   make([]uint64, 0),
			hashMap:     make(map[string]*ResolvedObject),
			packLength:  nObj,
			br:          br,
			file:        pf,
			fileInfo:    fileInfo,
			zr:          zr,
			hasher:      sha1.New(),
			workingDir:  workingDir,
		}

		builder.BuildIndex()

		for _, offset := range builder.packOrder {
			node := builder.packIndex[offset]

			resolved := builder.buildObject(node)
			builder.hashMap[resolved.SHA1] = resolved
			objDirName, objFileName := hashes.DecomposeHash(resolved.SHA1)

			err = os.MkdirAll(fp.Join(git.GitObjDir, objDirName), 0775)
			if err != nil && !errors.Is(err, fs.ErrExist) {
				fmt.Fprintf(os.Stderr, "Error creating dir: %v\n", err)
				os.Exit(1)
			}

			tmpf, err := os.CreateTemp(git.GitObjDir, "tmp_obj_")
			if err != nil {
				fmt.Println("Error creating temp file")
			}
			defer os.Remove(tmpf.Name())
			zw := builder.getZlibWriter(tmpf)
			zw.Write(TypeToBytes(resolved.Type))
			zw.Write([]byte{' '})
			zw.Write(fmt.Appendf(nil, "%d", len(resolved.Data)))
			zw.Write([]byte{'\x00'})
			zw.Write(resolved.Data)
			zw.Close()

			objFilePath := fp.Join(git.GitObjDir, objDirName, objFileName)
			if err = os.Rename(tmpf.Name(), objFilePath); err != nil {
				fmt.Fprintf(os.Stderr, "Error creating object: %v\n", err)
				os.Exit(1)
			}
		}
		// Get rid of the cache at this point, we don't need it anymore
		builder.lookupCache = nil
		runtime.GC()

		remoteHeadSHA := refs["HEAD"]
		var localDefaultBranch string
		// create the .git/refs directory
		for ref, SHA := range refs {
			if ref != "HEAD" && slices.Equal(SHA, remoteHeadSHA) {
				localDefaultBranch = ref
			}

			localRef := strings.Replace(ref, "refs/heads/", "refs/remotes/origin/", 1)

			destPath := fp.Join(".git", localRef)
			parentDir := fp.Dir(destPath)

			if err := os.MkdirAll(parentDir, 0755); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				continue
			}

			// 4. Write the SHA as the file content
			// 'object' is the SHA-1 hash (40 bytes)
			if err := os.WriteFile(destPath, append(SHA, '\n'), 0644); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			}
		}

		if localDefaultBranch != "" {
			// Set the remote HEAD ref
			localRef := strings.Replace(localDefaultBranch, "refs/heads/", "refs/remotes/origin/", 1)
			originHeadPath := fp.Join(".git", "refs/remotes/origin/HEAD")
			symbolicContent := fmt.Sprintf("ref: %s\n", localRef)
			os.WriteFile(originHeadPath, []byte(symbolicContent), 0644)

			// Create the LOCAL branch ref (e.g., .git/refs/heads/main)
			localBranchPath := fp.Join(".git", localDefaultBranch)
			os.MkdirAll(fp.Dir(localBranchPath), 0755)
			os.WriteFile(localBranchPath, append(remoteHeadSHA, '\n'), 0644)
			rootHeadPath := fp.Join(".git", "HEAD")
			rootHeadContent := fmt.Sprintf("ref: %s\n", localDefaultBranch)
			os.WriteFile(rootHeadPath, []byte(rootHeadContent), 0644)
		}

		// Checkout into the current HEAD
		headSHA := refs["HEAD"]
		// Read it from the hashMap
		checkoutResult := builder.checkoutRepository(headSHA)
		indexEntries := checkoutResult.indexEntries

		slices.SortFunc(indexEntries, func(a, b *IndexEntry) int {
			return strings.Compare(a.Path, b.Path)
		})

		tmpf, _ := os.CreateTemp(git.GitDir, "tmp_index_")
		defer os.Remove(tmpf.Name())
		sha := sha1.New()
		mw := io.MultiWriter(tmpf, sha)
		mw.Write([]byte{'D', 'I', 'R', 'C'})
		binary.Write(mw, binary.BigEndian, uint32(2))
		binary.Write(mw, binary.BigEndian, uint32(len(indexEntries)))
		for _, entry := range indexEntries {
			writeGitIndexLine(entry, mw)
		}
		h := sha.Sum(nil)
		tmpf.Write(h)

		if err = os.Rename(tmpf.Name(), git.GitIndexPath); err != nil {
			fmt.Fprintf(os.Stderr, "Error creating object: %v\n", err)
			os.Exit(1)
		}

	default:
		fmt.Fprintf(os.Stderr, "Unknown command %s\n", command)
		os.Exit(1)
	}
}

type PackStreamer struct {
	f     *os.File
	fInfo os.FileInfo
	br    *bufio.Reader
	zr    io.ReadCloser
	zbr   *bufio.Reader
}

func (streamer *PackStreamer) getZlibReader() *bufio.Reader {
	if streamer.zr == nil {
		streamer.zr, _ = zlib.NewReader(streamer.br)
		streamer.zbr = bufio.NewReader(streamer.zr)
	} else {
		streamer.zr.(zlib.Resetter).Reset(streamer.br, nil)
		streamer.zbr.Reset(streamer.zr)
	}
	return streamer.zbr
}

func (streamer *PackStreamer) BuildPackIndex(objectCount uint32) ([]uint64, map[uint64]PackNode) {
	packOrder := make([]uint64, 0, objectCount)
	packIndex := make(map[uint64]PackNode)
	for i := 1; uint32(i) <= objectCount; i++ {
		headerOfs := currentOffset(streamer.f, streamer.br)
		objType, objSize := readObjHeader(streamer.br)
		packOrder = append(packOrder, headerOfs)

		var parentOfs uint64
		if objType == 6 {
			// The required negative offet brom the type byte
			negOfs := readDeltaNegOfs(streamer.br)
			parentOfs = uint64(headerOfs) - negOfs
		}

		dataOfs := currentOffset(streamer.f, streamer.br)

		if objType == 6 {
			srcBufSize, dstBufSize, ops := streamer.parseDeltaObj()
			packIndex[headerOfs] = &DeltaNode{
				srcBufSize: srcBufSize,
				dstBufSize: dstBufSize,
				parentOfs:  parentOfs,
				ops:        ops,
				objSize:    objSize,
				headerOfs:  headerOfs,
			}
		} else {
			io.Copy(io.Discard, streamer.getZlibReader())
			packIndex[headerOfs] = &ObjectNode{
				objType:    objType,
				objSize:    objSize,
				headerOfs:  headerOfs,
				dataOffset: dataOfs,
			}
		}
	}
	return packOrder, packIndex
}

func (streamer *PackStreamer) parseDeltaObj() (srcBufSize, dstBufSize uint64, ops []DeltaOps) {
	zbr := streamer.getZlibReader()
	srcSize, dstSize := readDeltaHeader(zbr)
	for {
		b, err := zbr.ReadByte()
		if errors.Is(err, io.EOF) {
			break
		}
		if b&0x80 != 0 {
			copyOfsFlags := (b & git.CopyOffsetFlagsMask)
			copySizeFlags := (b & git.CopySizeFlagsMask) >> git.CopySizeFlagsShift
			ofs := readDeltaCopyOffset(copyOfsFlags, zbr)
			size := readDeltaCopySize(copySizeFlags, zbr)
			ops = append(ops, CopyOp{Offset: ofs, Size: size})
		} else {
			payloadSize := (b & git.InsertSizeMask)
			insertPayloadBuf := make([]byte, payloadSize)
			io.ReadFull(zbr, insertPayloadBuf)
			ops = append(ops, InsertOp{PayloadSize: payloadSize, Payload: insertPayloadBuf})
		}
	}
	return srcSize, dstSize, ops
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

func writeGitIndexLine(e *IndexEntry, w io.Writer) {
	binary.Write(w, binary.BigEndian, e.CtimeSec)
	binary.Write(w, binary.BigEndian, e.CtimeNano)
	binary.Write(w, binary.BigEndian, e.MtimeSec)
	binary.Write(w, binary.BigEndian, e.MtimeNano)
	binary.Write(w, binary.BigEndian, e.Dev)
	binary.Write(w, binary.BigEndian, e.Ino)
	binary.Write(w, binary.BigEndian, e.Mode)
	binary.Write(w, binary.BigEndian, e.UID)
	binary.Write(w, binary.BigEndian, e.GID)
	binary.Write(w, binary.BigEndian, e.Size)
	w.Write(e.SHA[:])
	binary.Write(w, binary.BigEndian, e.Flags)

	// Write the path
	w.Write([]byte(e.Path))

	// Padding logic: Git requires 1-8 null bytes to reach
	// a total entry length that is a multiple of 8.
	entryLenSoFar := 62 + len(e.Path)
	padding := 8 - (entryLenSoFar % 8)
	if padding == 0 {
		padding = 8
	}
	w.Write(make([]byte, padding))
}

func TranslateGitModeToUint(gitMode string) uint32 {
	switch gitMode {
	case "100644":
		return 33188 // Octal 0100644
	case "100755":
		return 33261 // Octal 0100755
	default:
		return 33188
	}
}

type checkoutResult struct {
	indexEntries []*IndexEntry
}

// TranslateGitMode converts Git mode strings to standard Unix os.FileMode
func TranslateGitMode(gitMode string) os.FileMode {
	switch gitMode {
	case "100755":
		// Executable file
		return 0755
	case "100644":
		// Regular non-executable file
		return 0644
	default:
		// Default fallback for safety (regular file)
		return 0644
	}
}

type TreeEntry struct {
	mode string
	name string
	sha1 string
}

func returnCommitTreeSHA(commitData []byte) string {
	return string(commitData[5:45])
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

func isLastTreeEntry(treeData []byte, delimIdx int) bool {
	return len(treeData[delimIdx+1:]) <= 20
}

func prepareReq(buf *bytes.Buffer, refs map[string][]byte, negotiated []byte) {
	for ref, pkt := range refs {
		var pktPayload string
		if ref == "HEAD" {
			pktPayload = fmt.Sprintf("want %s %s\n", pkt, negotiated)
		} else {
			pktPayload = fmt.Sprintf("want %s\n", pkt)
		}
		pktHeader := fmt.Sprintf("%04x", len(pktPayload)+4)
		buf.WriteString(pktHeader)
		buf.WriteString(pktPayload)
	}
	buf.WriteString("0000")
	buf.WriteString("0009done\n")
}

func validateUploadPackResponse(br *bufio.Reader) error {
	firstFive, _ := br.Peek(5)
	if matched, _ := regexp.Match("^[0-9a-f]", firstFive); !matched {
		return fmt.Errorf("Invalid response")
	}

	// Read till the first flush packet
	svcName, _ := readPktLine(br)
	if !bytes.Equal(svcName, []byte("# service=git-upload-pack")) {
		return fmt.Errorf("Invalid response")
	}
	return nil
}

// Return the pktline without any trailing new-line or nul bytes
func readPktLine(r io.Reader) ([]byte, error) {
	pktHeader := make([]byte, 4)
	_, err := io.ReadFull(r, pktHeader)
	if err != nil {
		return nil, err
	}
	pktLength, _ := strconv.ParseInt(string(pktHeader), 16, 32)
	if pktLength == 0 {
		return nil, nil
	}
	out := make([]byte, pktLength-4)
	_, err = io.ReadFull(r, out)
	if err != nil {
		return nil, err
	}
	return bytes.TrimRight(out, "\n\x00"), nil
}

type PackStats struct {
	NonDeltaCount int
	ChainCounts   map[int]int
}

func (s *PackStats) ProcessResult(res *ObjectResult) {
	printResult(res) // Print as we go
	s.Observe(res)   // Update counters
}

func (s *PackStats) Observe(res *ObjectResult) {
	if res.Depth == 0 {
		s.NonDeltaCount++
	} else {
		s.ChainCounts[res.Depth]++
	}
}

func (s *PackStats) PrintSummary() {
	fmt.Printf("non delta: %d objects\n", s.NonDeltaCount)

	depths := make([]int, 0, len(s.ChainCounts))
	for d := range s.ChainCounts {
		depths = append(depths, d)
	}
	sort.Ints(depths)

	for _, d := range depths {
		count := s.ChainCounts[d]
		fmt.Printf("chain length = %d: %d objects\n", d, count)
	}
}

type PackStatsCopy struct {
	NonDeltaCount int
	ChainCounts   map[int]int
	Results       []*ObjectResult
}

func readPackHeader(r io.Reader) (version, objectCount uint32, err error) {
	buf := make([]byte, 12)

	// Discard the first 4 bytes
	io.ReadFull(r, buf)

	if string(buf[:4]) != "PACK" {
		return 0, 0, fmt.Errorf("not a valid pack file")
	}
	// Read the version and the nobjects
	version = binary.BigEndian.Uint32(buf[4:8])
	objectCount = binary.BigEndian.Uint32(buf[8:12])
	return version, objectCount, nil
}

func verifyPackTrailer(f *os.File, fInfo os.FileInfo, h hash.Hash) error {
	defer h.Reset()
	defer f.Seek(0, io.SeekStart)

	if fInfo.Size() < 32 {
		return fmt.Errorf("packfile is too small to be valid")
	}

	expected := make([]byte, 20)
	_, err := f.ReadAt(expected, fInfo.Size()-20)
	if err != nil {
		return fmt.Errorf("failed to read expected trailer: %w", err)
	}

	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}

	lr := io.LimitReader(f, fInfo.Size()-20)

	if _, err := io.Copy(h, lr); err != nil {
		return fmt.Errorf("failed to hash pack content: %w", err)
	}

	actual := h.Sum(nil)
	if !bytes.Equal(expected, actual) {
		return fmt.Errorf("invalid packfile: checksum doesn't match")
	}
	return nil
}

func printResult(res *ObjectResult) {
	if res.Depth > 0 {
		// Format: SHA1 TYPE SIZE PACKSIZE OFFSET DEPTH PARENT_SHA1
		fmt.Printf("%s %-7s %d %d %d %d %s\n",
			res.SHA1,
			TypeToBytes(res.Type),
			res.Size,
			res.PackSize,
			res.Offset,
			res.Depth,
			res.ParentSHA1,
		)
	} else {
		// Format: SHA1 TYPE SIZE PACKSIZE OFFSET
		fmt.Printf("%s %-7s %d %d %d\n",
			res.SHA1,
			TypeToBytes(res.Type),
			res.Size,
			res.PackSize,
			res.Offset,
		)
	}
}

type copyBuilder struct {
	packIndex   map[uint64]PackNode
	packOrder   []uint64
	packLength  uint32
	lookupCache map[uint64]*ResolvedObject
	hashMap     map[string]*ResolvedObject
	file        *os.File
	fileInfo    os.FileInfo
	br          *bufio.Reader
	zr          io.ReadCloser
	zw          *zlib.Writer
	hasher      hash.Hash
	workingDir  string
}

// TODO: add verification. This can only be run when some info about the pack is already known.
func (builder *copyBuilder) ComputePackStats() PackStatsCopy {
	stats := PackStatsCopy{
		ChainCounts: make(map[int]int),
		Results:     make([]*ObjectResult, 0, len(builder.packOrder)),
	}

	for i, offset := range builder.packOrder {
		node := builder.packIndex[offset]

		var nxtOfs uint64
		if i < len(builder.packOrder)-1 {
			nxtOfs = builder.packOrder[i+1]
		} else {
			nxtOfs = uint64(builder.fileInfo.Size() - 20)
		}

		result := builder.resolveObject(node, offset, nxtOfs)

		// Collect results for printing later
		stats.Results = append(stats.Results, result)

		// Update stats
		if result.Depth == 0 {
			stats.NonDeltaCount++
		} else {
			stats.ChainCounts[result.Depth]++
		}
	}
	return stats
}

func (builder *copyBuilder) BuildIndex() {
	for i := 1; uint32(i) <= builder.packLength; i++ {
		headerOfs := currentOffset(builder.file, builder.br)
		objType, objSize := readObjHeader(builder.br)
		builder.packOrder = append(builder.packOrder, headerOfs)

		var parentOfs uint64
		if objType == 6 {
			// The required negative offet from the type byte
			negOfs := readDeltaNegOfs(builder.br)
			parentOfs = uint64(headerOfs) - negOfs
		}

		dataOfs := currentOffset(builder.file, builder.br)
		if builder.zr == nil {
			builder.zr, _ = zlib.NewReader(builder.br)
		} else {
			builder.zr.(zlib.Resetter).Reset(builder.br, nil)
		}

		if objType == 6 {
			var buf bytes.Buffer
			srcBufSize, dstBufSize, ops := parseDeltaObjCopy(&buf, builder.zr)
			builder.packIndex[headerOfs] = &DeltaNode{
				srcBufSize: srcBufSize,
				dstBufSize: dstBufSize,
				parentOfs:  parentOfs,
				ops:        ops,
				objSize:    objSize,
				headerOfs:  headerOfs,
			}
		} else {
			io.Copy(io.Discard, builder.zr)
			builder.packIndex[headerOfs] = &ObjectNode{
				objType:    objType,
				objSize:    objSize,
				headerOfs:  headerOfs,
				dataOffset: dataOfs,
			}
		}
	}
}

func (builder *copyBuilder) getZlibWriter(w io.Writer) *zlib.Writer {
	if builder.zw == nil {
		builder.zw = zlib.NewWriter(w)
	} else {
		builder.zw.Reset(w)
	}
	return builder.zw
}

func (builder *copyBuilder) Index() map[uint64]PackNode {
	return builder.packIndex
}

func (builder *copyBuilder) resolveObject(n PackNode, currHeaderOfs, nxtHeaderOfs uint64) *ObjectResult {
	resolvedObject := builder.buildObject(n)
	return &ObjectResult{
		SHA1:       resolvedObject.SHA1,
		Type:       resolvedObject.Type,
		Size:       n.ObjectSize(),
		PackSize:   nxtHeaderOfs - currHeaderOfs,
		Offset:     currHeaderOfs,
		ParentSHA1: resolvedObject.ParentSHA1,
		Depth:      resolvedObject.Depth,
	}
}

func (builder *copyBuilder) resolveDelta(n *DeltaNode) *ResolvedObject {
	parentResolvedObject := builder.buildObject(builder.Index()[n.ParentOffset()])
	depth := parentResolvedObject.Depth + 1
	baseType := parentResolvedObject.Type
	data := applyDelta(n, parentResolvedObject.Data)
	hash := builder.ReturnObjectSHA(data, int64(len(data)), baseType)
	res := &ResolvedObject{
		SHA1:       hash,
		Depth:      depth,
		ParentSHA1: parentResolvedObject.SHA1,
		Type:       baseType,
		Data:       data,
	}
	return res
}

func (builder *copyBuilder) ReturnObjectSHA(data []byte, size int64, objType uint8) string {
	if builder.hasher == nil {
		builder.hasher = sha1.New()
	} else {
		builder.hasher.Reset()
	}
	sha := sha1.New()
	sha.Write(TypeToBytes(objType))
	sha.Write([]byte(" "))
	sha.Write([]byte(strconv.FormatInt(int64(len(data)), 10)))
	sha.Write([]byte{0})
	sha.Write(data)

	h := hex.EncodeToString(sha.Sum(nil))
	return h
}

func (builder *copyBuilder) resolveBase(n *ObjectNode) *ResolvedObject {
	data := builder.readObjectData(n)
	hash := builder.ReturnObjectSHA(data, int64(n.objSize), n.objType)
	objType := n.Type()
	depth := 0
	res := &ResolvedObject{
		SHA1:       hash,
		Depth:      depth,
		ParentSHA1: "",
		Type:       objType,
		Data:       data,
	}
	return res
}

func (builder *copyBuilder) buildObject(n PackNode) *ResolvedObject {
	if cached, ok := builder.lookupCache[n.Offset()]; ok {
		return cached
	}

	var res *ResolvedObject

	switch Type := n.Type(); Type {
	case 6:
		res = builder.resolveDelta(n.(*DeltaNode))
	default:
		res = builder.resolveBase(n.(*ObjectNode))
	}
	builder.lookupCache[n.Offset()] = res

	return res
}

func (builder *copyBuilder) readObjectData(n *ObjectNode) []byte {
	f := builder.file
	br := builder.br
	f.Seek(int64(n.dataOffset), 0)
	br.Reset(f)
	if builder.zr == nil {
		builder.zr, _ = zlib.NewReader(builder.br)
	} else {
		if reseter, ok := builder.zr.(zlib.Resetter); ok {
			reseter.Reset(builder.br, nil)
		}
	}
	buf := make([]byte, n.ObjectSize())
	io.ReadFull(builder.zr, buf)
	return buf
}

func (builder *copyBuilder) checkoutRepository(headSHA []byte) *checkoutResult {
	result := &checkoutResult{indexEntries: make([]*IndexEntry, 0, 32)}
	headCommit := builder.hashMap[string(headSHA)]
	treeSHA := returnCommitTreeSHA(headCommit.Data)
	treeData := builder.hashMap[treeSHA].Data
	builder.buildRepository("", treeData, result)
	return result
}

// When I see a tree:
// 1. I need to create a directory for it.
// 2. I need to parse the contents of the tree into it's own directory
// 3. Repeat
func (builder *copyBuilder) buildRepository(workingDir string, treeData []byte, result *checkoutResult) {
	treeEntries := parseTree(treeData)
	for _, treeEntry := range treeEntries {
		if treeEntry.mode != git.GitDirMode {
			// write the files
			filepath := fp.Join(workingDir, treeEntry.name)
			os.WriteFile(filepath, builder.hashMap[treeEntry.sha1].Data, TranslateGitMode(treeEntry.mode))
			fileInfo, _ := os.Lstat(filepath)
			shaBytes, _ := hex.DecodeString(treeEntry.sha1)
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
			curDir := fp.Join(workingDir, treeEntry.name)
			os.MkdirAll(curDir, 0755)
			treeData := builder.hashMap[treeEntry.sha1].Data
			builder.buildRepository(fp.Join(workingDir, treeEntry.name), treeData, result)
		}
	}
}

type objectBuilder struct {
	packIndex   map[uint64]PackNode
	lookupCache map[uint64]*ResolvedObject
	hashMap     map[string]*ResolvedObject
	packOrder   []uint64
	file        *os.File
	fileInfo    os.FileInfo
	br          *bufio.Reader
	zr          io.ReadCloser
	hasher      hash.Hash
	packLength  uint32
}

// TODO: add verification. This can only be run when some info about the pack is already known.
func (builder *objectBuilder) ForEachObject(fn func(res *ObjectResult)) {
	for i, offset := range builder.packOrder {
		node := builder.packIndex[offset]

		var nxtOfs uint64
		if i < len(builder.packOrder)-1 {
			nxtOfs = builder.packOrder[i+1]
		} else {
			nxtOfs = uint64(builder.fileInfo.Size() - 20)
		}

		result := builder.resolveObject(node, offset, nxtOfs)

		fn(result)
	}
}

func (builder *objectBuilder) Index() map[uint64]PackNode {
	return builder.packIndex
}

func (builder *objectBuilder) resolveObject(n PackNode, currHeaderOfs, nxtHeaderOfs uint64) *ObjectResult {
	resolvedObject := builder.buildObject(n)
	return &ObjectResult{
		SHA1:       resolvedObject.SHA1,
		Type:       resolvedObject.Type,
		Size:       n.ObjectSize(),
		PackSize:   nxtHeaderOfs - currHeaderOfs,
		Offset:     currHeaderOfs,
		ParentSHA1: resolvedObject.ParentSHA1,
		Depth:      resolvedObject.Depth,
	}
}

func (builder *objectBuilder) resolveDelta(n *DeltaNode) *ResolvedObject {
	parentResolvedObject := builder.buildObject(builder.Index()[n.ParentOffset()])
	depth := parentResolvedObject.Depth + 1
	baseType := parentResolvedObject.Type
	data := applyDelta(n, parentResolvedObject.Data)
	hash := builder.ReturnObjectSHA(data, int64(len(data)), baseType)
	res := &ResolvedObject{
		SHA1:       hash,
		Depth:      depth,
		ParentSHA1: parentResolvedObject.SHA1,
		Type:       baseType,
		Data:       data,
	}
	return res
}

func (builder *objectBuilder) ReturnObjectSHA(data []byte, size int64, objType uint8) string {
	if builder.hasher == nil {
		builder.hasher = sha1.New()
	} else {
		builder.hasher.Reset()
	}
	sha := sha1.New()
	sha.Write(TypeToBytes(objType))
	sha.Write([]byte(" "))
	sha.Write([]byte(strconv.FormatInt(int64(len(data)), 10)))
	sha.Write([]byte{0})
	sha.Write(data)

	h := hex.EncodeToString(sha.Sum(nil))
	return h
}

func (builder *objectBuilder) resolveBase(n *ObjectNode) *ResolvedObject {
	data := builder.readObjectData(n)
	hash := builder.ReturnObjectSHA(data, int64(n.objSize), n.objType)
	objType := n.Type()
	depth := 0
	res := &ResolvedObject{
		SHA1:       hash,
		Depth:      depth,
		ParentSHA1: "",
		Type:       objType,
		Data:       data,
	}
	return res
}

func (builder *objectBuilder) buildObject(n PackNode) *ResolvedObject {
	if cached, ok := builder.lookupCache[n.Offset()]; ok {
		return cached
	}

	var res *ResolvedObject

	switch Type := n.Type(); Type {
	case 6:
		res = builder.resolveDelta(n.(*DeltaNode))
	default:
		res = builder.resolveBase(n.(*ObjectNode))
	}
	builder.lookupCache[n.Offset()] = res

	return res
}

func (builder *objectBuilder) readObjectData(n *ObjectNode) []byte {
	f := builder.file
	br := builder.br
	f.Seek(int64(n.dataOffset), 0)
	br.Reset(f)
	if builder.zr == nil {
		builder.zr, _ = zlib.NewReader(builder.br)
	} else {
		if reseter, ok := builder.zr.(zlib.Resetter); ok {
			reseter.Reset(builder.br, nil)
		}
	}
	buf := make([]byte, n.ObjectSize())
	io.ReadFull(builder.zr, buf)
	return buf
}

func (builder *objectBuilder) checkoutRepository(headSHA []byte) *checkoutResult {
	result := &checkoutResult{indexEntries: make([]*IndexEntry, 0, 32)}
	headCommit := builder.hashMap[string(headSHA)]
	treeSHA := returnCommitTreeSHA(headCommit.Data)
	treeData := builder.hashMap[treeSHA].Data
	builder.buildRepository("", treeData, result)
	return result
}

// When I see a tree:
// 1. I need to create a directory for it.
// 2. I need to parse the contents of the tree into it's own directory
// 3. Repeat
func (builder *objectBuilder) buildRepository(workingDir string, treeData []byte, result *checkoutResult) {
	treeEntries := parseTree(treeData)
	for _, treeEntry := range treeEntries {
		if treeEntry.mode != git.GitDirMode {
			// write the files
			filepath := fp.Join(workingDir, treeEntry.name)
			os.WriteFile(filepath, builder.hashMap[treeEntry.sha1].Data, TranslateGitMode(treeEntry.mode))
			fileInfo, _ := os.Lstat(filepath)
			shaBytes, _ := hex.DecodeString(treeEntry.sha1)
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
			curDir := fp.Join(workingDir, treeEntry.name)
			os.MkdirAll(curDir, 0755)
			treeData := builder.hashMap[treeEntry.sha1].Data
			builder.buildRepository(fp.Join(workingDir, treeEntry.name), treeData, result)
		}
	}
}

type ObjectResult struct {
	SHA1       string
	Type       uint8
	Size       uint64 // The uncompressed size of the object payload
	PackSize   uint64
	Offset     uint64
	Depth      int
	ParentSHA1 string
}

func (res *ObjectResult) TypeName() []byte {
	return TypeToBytes(res.Type)
}

type ResolvedObject struct {
	SHA1       string
	Type       uint8
	Depth      int
	ParentSHA1 string
	Data       []byte
}

func applyDelta(d *DeltaNode, srcBuf []byte) []byte {
	if len(srcBuf) != int(d.srcBufSize) {
		fmt.Fprintf(os.Stderr, "Unexepected src buffer size")
		os.Exit(1)
	}
	dstBuf := make([]byte, d.dstBufSize)
	var cursor uint64
	for _, op := range d.ops {
		if op.kind() == 1 {
			copyOp, ok := op.(CopyOp)
			if !ok {
				fmt.Fprintf(os.Stderr, "Something is very wrong")
				os.Exit(1)
			}
			// Copy from the given offset into the dst buffer
			copy(dstBuf[cursor:], srcBuf[copyOp.Offset:copyOp.Offset+copyOp.Size])
			cursor += copyOp.Size
		} else {
			insertOp, ok := op.(InsertOp)
			if !ok {
				fmt.Fprintf(os.Stderr, "Something is very wrong")
				os.Exit(1)
			}
			copy(dstBuf[cursor:], insertOp.Payload)
			cursor += uint64(len(insertOp.Payload))
		}

	}
	return dstBuf
}

func currentOffset(f *os.File, br *bufio.Reader) uint64 {
	fofs, _ := f.Seek(0, 1)
	currOfs := fofs - int64(br.Buffered())

	return uint64(currOfs)
}

type PackNode interface {
	Type() uint8
	ParentOffset() uint64
	ObjectSize() uint64
	Offset() uint64
}

type ObjectNode struct {
	objSize    uint64
	headerOfs  uint64
	dataOffset uint64 // The offset from the headerOfs to find the data, can be a maximum of 64 bits -> 8 bytes -> can be stored in uint8
    objType    uint8
}

func (n *ObjectNode) Type() uint8 {
	return n.objType
}

func (n *ObjectNode) ParentOffset() uint64 {
	return 0
}

func (n *ObjectNode) Offset() uint64 {
	return n.headerOfs
}

func (n *ObjectNode) ObjectSize() uint64 {
	return n.objSize
}

type DeltaNode struct {
	srcBufSize uint64
	dstBufSize uint64
	parentOfs  uint64
	objSize    uint64
	headerOfs  uint64
    ops        []DeltaOps
}

func (n *DeltaNode) Type() uint8 {
	return git.OBJ_OFS_DELTA
}

func (n *DeltaNode) ParentOffset() uint64 {
	return n.parentOfs
}

func (n *DeltaNode) ObjectSize() uint64 {
	return n.objSize
}

func (n *DeltaNode) Offset() uint64 {
	return n.headerOfs
}

type DeltaOps interface {
	kind() byte
}

type CopyOp struct {
	Offset uint64
	Size   uint64
}

func (CopyOp) kind() byte {
	return OP_CODE_COPY
}

type InsertOp struct {
	Payload     []byte
    PayloadSize uint8
}

func (InsertOp) kind() byte {
	return OP_COPE_INSERT
}

func parseDeltaObjCopy(buf *bytes.Buffer, zr io.ReadCloser) (srcBufSize, dstBufSize uint64, ops []DeltaOps) {
	io.Copy(buf, zr)
	srcSize, dstSize := readDeltaHeaderCopy(buf)

	for buf.Len() != 0 {
		b, _ := (buf).ReadByte()
		if b&0x80 != 0 {
			copyOfsFlags := (b & git.CopyOffsetFlagsMask)
			copySizeFlags := (b & git.CopySizeFlagsMask) >> git.CopySizeFlagsShift
			ofs := readDeltaCopyOffsetCopy(copyOfsFlags, buf)
			size := readDeltaCopySizeCopy(copySizeFlags, buf)
			ops = append(ops, CopyOp{Offset: ofs, Size: size})
		} else {
			payloadSize := (b & git.InsertSizeMask)
			insertPayloadBuf := make([]byte, payloadSize)
			io.ReadFull(buf, insertPayloadBuf)
			ops = append(ops, InsertOp{PayloadSize: payloadSize, Payload: insertPayloadBuf})
		}
	}
	return srcSize, dstSize, ops
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
func readDeltaHeader(r *bufio.Reader) (srcSize uint64, dstSize uint64) {
	srcSize = readDeltaSize(r)
	dstSize = readDeltaSize(r)
	return srcSize, dstSize
}

func readDeltaHeaderCopy(buf *bytes.Buffer) (srcSize uint64, dstSize uint64) {
	srcSize = readDeltaSizeCopy(buf)
	dstSize = readDeltaSizeCopy(buf)
	return srcSize, dstSize
}

func readDeltaSizeCopy(buf *bytes.Buffer) uint64 {
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

func readDeltaSize(r *bufio.Reader) uint64 {
	var i uint64
	var size uint64
	i++
	for {
		b, _ := r.ReadByte()
		size = uint64(b&0b01111111)<<((i-1)*7) | size
		if b&0x80 == 0 {
			break
		}
		i++
	}
	return size
}

func readDeltaCopyOffset(ofsFlags byte, br *bufio.Reader) (ofs uint64) {
	for i := range git.CopyOffsetFlagsLen {
		if (0b00000001 & (ofsFlags >> i)) == 1 {
			b, _ := br.ReadByte()
			ofs |= uint64(b) << (8 * i)
		}
	}
	return ofs
}

func readDeltaCopySize(sizeFlags byte, br *bufio.Reader) (size uint64) {
	for i := range git.CopySizeFlagsLen {
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

func readDeltaCopyOffsetCopy(ofsFlags byte, br *bytes.Buffer) (ofs uint64) {
	for i := range git.CopyOffsetFlagsLen {
		if (0b00000001 & (ofsFlags >> i)) == 1 {
			b, _ := br.ReadByte()
			ofs |= uint64(b) << (8 * i)
		}
	}
	return ofs
}

func readDeltaCopySizeCopy(sizeFlags byte, br *bytes.Buffer) (size uint64) {
	for i := range git.CopySizeFlagsLen {
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

func TypeToBytes(input uint8) []byte {
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
		return nil
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

type TreeBuilder struct {
	zw   *zlib.Writer
	hash hash.Hash
}

// TODO: maybe use goroutines here for performance.
func (b *TreeBuilder) generateTreePayload(cwd []fs.DirEntry, currentPath string) *bytes.Buffer {
	var entries []GitTreeEntry
	for _, e := range cwd {
		if e.Name() == ".git" {
			continue
		} else if e.IsDir() {
			sd, err := os.ReadDir(fp.Join(currentPath, e.Name()))
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error walking current working directory: %v\n", err)
			}

			subtreePayload := b.generateTreePayload(sd, fp.Join(currentPath, e.Name()))

			hsum := b.writeAndGetHash("tree", subtreePayload.Len(), subtreePayload)

			entry := GitTreeEntry{
				Mode: git.GitDirMode,
				Name: e.Name(),
				Hash: hsum,
			}
			entries = append(entries, entry)

		} else if !e.IsDir() {

			f, finfo := files.OpenFile(fp.Join(currentPath, e.Name()))

			hsum := b.writeAndGetHash("blob", int(finfo.Size()), f)

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

	return buf
}

func (b *TreeBuilder) writeAndGetHash(objectType string, payloadSize int, payload io.Reader) []byte {
	tmpf := files.CreateTempObjFile()
	b.hash.Reset()
	b.zw.Reset(tmpf)
	mw := io.MultiWriter(b.hash, b.zw)
	files.WriteGitObject(mw, objectType, payloadSize, payload)
	b.zw.Close()
	tmpf.Close()

	hsum := b.hash.Sum(nil)
	h := hex.EncodeToString(hsum)
	objDirName, objFileName := hashes.DecomposeHash(h)
	err := os.MkdirAll(fp.Join(git.GitObjDir, objDirName), 0775)
	if err != nil && !errors.Is(err, fs.ErrExist) {
		fmt.Fprintf(os.Stderr, "Error creating dir: %v\n", err)
		os.Exit(1)
	}

	objFilePath := fp.Join(git.GitObjDir, objDirName, objFileName)
	if err := os.Rename(tmpf.Name(), objFilePath); err != nil {
		fmt.Fprintf(os.Stderr, "Error creating object: %v\n", err)
		os.Exit(1)
	}
	return hsum
}
