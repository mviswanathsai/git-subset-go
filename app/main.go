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
	"os"
	fp "path/filepath"
	"slices"
	"strconv"
	"strings"
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

	case "verify-pack":
		pfPath := os.Args[2]
		// Open the file instead for comparison
		pf, fileInfo := files.OpenFile(pfPath)

		br := bufio.NewReader(pf)
		_, nObj, err := readPackHeader(br)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Pack header invalid: %v", err)
			os.Exit(1)
		}

		var zr io.ReadCloser

		builder := &objectBuilder{
			packIndex:   make(map[uint64]packNode),
			lookupCache: make(map[uint64]*ResolvedObject),
			packOrder:   make([]uint64, 0),
			packLength:  nObj,
			br:          br,
			file:        pf,
			fileInfo:    fileInfo,
			zr:          zr,
			hasher:      sha1.New(),
		}

		builder.BuildIndex()

		// At this point, only 20 bytes should be left
		var fileHash []byte
		bytesLeft := uint64(fileInfo.Size()) - currentOffset(pf, br)
		if bytesLeft == 20 {
			fileHash, _ = io.ReadAll(br)
		} else {
			fmt.Fprintf(os.Stderr, "Expected 20 bytes unread, got %d bytes", bytesLeft)
		}

		stats := builder.ResolveAll()
		for _, res := range stats.Results {
			printResult(res)
		}

		fmt.Printf("non delta: %d objects\n", stats.NonDeltaCount)
		for depth := 1; ; depth++ {
			count, exists := stats.ChainCounts[depth]
			if !exists {
				break
			}
			fmt.Printf("chain length = %d: %d objects\n", depth, count)
		}
		if verifyPackTrailer(pf, fileInfo, fileHash) {
			fmt.Printf("%s: ok\n", pfPath)
		}

	default:
		fmt.Fprintf(os.Stderr, "Unknown command %s\n", command)
		os.Exit(1)
	}
}

func (builder *objectBuilder) ResolveAll() PackStats {
	stats := PackStats{
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

type PackStats struct {
	NonDeltaCount int
	ChainCounts   map[int]int
	Results       []*ObjectResult
}

func (builder *objectBuilder) BuildIndex() {
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
			srcBufSize, dstBufSize, ops := parseDeltaObj(&buf, builder.zr)
			builder.packIndex[headerOfs] = &deltaNode{
				srcBufSize: srcBufSize,
				dstBufSize: dstBufSize,
				parentOfs:  parentOfs,
				ops:        ops,
				objSize:    objSize,
				headerOfs:  headerOfs,
			}
		} else {
			io.Copy(io.Discard, builder.zr)
			builder.packIndex[headerOfs] = &objectNode{
				objType:    objType,
				objSize:    objSize,
				headerOfs:  headerOfs,
				dataOffset: dataOfs,
			}
		}
	}
}

func readPackHeader(r io.Reader) (uint32, uint32, error) {
	buf := make([]byte, 12)

	// Discard the first 4 bytes
	io.ReadFull(r, buf)

	if string(buf[:4]) != "PACK" {
		return 0, 0, fmt.Errorf("not a valid pack file")
	}
	// Read the version and the nobjects
	version := binary.BigEndian.Uint32(buf[4:8])
	nObj := binary.BigEndian.Uint32(buf[8:12])
	return version, nObj, nil
}

func verifyPackTrailer(f *os.File, fInfo os.FileInfo, fileHash []byte) bool {
	f.Seek(0, 0)
	h := sha1.New()
	lr := io.LimitReader(f, fInfo.Size()-20)

	data, _ := io.Copy(h, lr)
	if data < 20 {
		os.Exit(1)
	}

	actual := h.Sum(nil)

	return bytes.Equal(fileHash, actual)
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

type objectBuilder struct {
	packIndex   map[uint64]packNode
	packOrder   []uint64
	packLength  uint32
	lookupCache map[uint64]*ResolvedObject
	file        *os.File
	fileInfo    os.FileInfo
	br          *bufio.Reader
	zr          io.ReadCloser
	hasher      hash.Hash
}

func (builder *objectBuilder) Index() map[uint64]packNode {
	return builder.packIndex
}

func (builder *objectBuilder) resolveObject(n packNode, currHeaderOfs, nxtHeaderOfs uint64) *ObjectResult {
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

func (builder *objectBuilder) resolveDelta(n *deltaNode) *ResolvedObject {
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

func (builder *objectBuilder) resolveBase(n *objectNode) *ResolvedObject {
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

func (builder *objectBuilder) buildObject(n packNode) *ResolvedObject {
	if cached, ok := builder.lookupCache[n.Offset()]; ok {
		return cached
	}

	var res *ResolvedObject

	switch Type := n.Type(); Type {
	case 6:
		res = builder.resolveDelta(n.(*deltaNode))
	default:
		res = builder.resolveBase(n.(*objectNode))
	}
	builder.lookupCache[n.Offset()] = res

	return res
}

func (builder *objectBuilder) readObjectData(n *objectNode) []byte {
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

func applyDelta(d *deltaNode, srcBuf []byte) []byte {
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

type packNode interface {
	Type() uint8
	ParentOffset() uint64
	ObjectSize() uint64
	Offset() uint64
}

type objectNode struct {
	objType    uint8
	objSize    uint64
	headerOfs  uint64
	dataOffset uint64 // The offset from the headerOfs to find the data, can be a maximum of 64 bits -> 8 bytes -> can be stored in uint8
}

func (n *objectNode) Type() uint8 {
	return n.objType
}

func (n *objectNode) ParentOffset() uint64 {
	return 0
}

func (n *objectNode) Offset() uint64 {
	return n.headerOfs
}

func (n *objectNode) ObjectSize() uint64 {
	return n.objSize
}

type deltaNode struct {
	srcBufSize uint64
	dstBufSize uint64
	parentOfs  uint64
	ops        []DeltaOps
	objSize    uint64
	headerOfs  uint64
}

func (n *deltaNode) Type() uint8 {
	return git.OBJ_OFS_DELTA
}

func (n *deltaNode) ParentOffset() uint64 {
	return n.parentOfs
}

func (n *deltaNode) ObjectSize() uint64 {
	return n.objSize
}

func (n *deltaNode) Offset() uint64 {
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
	PayloadSize uint8
	Payload     []byte
}

func (InsertOp) kind() byte {
	return OP_COPE_INSERT
}

func parseDeltaObj(buf *bytes.Buffer, zr io.ReadCloser) (srcBufSize, dstBufSize uint64, ops []DeltaOps) {
	// mw := io.MultiWriter(dstWriter, &buf)
	io.Copy(buf, zr)
	srcSize, dstSize := readDeltaHeader(buf)
	////fmt.Fprintf(dstWriter, "\n")
	////fmt.Fprintf(dstWriter, "src buffer size:%d\ndst buffer size:%d\n", srcSize, dstSize)
	////fmt.Fprintf(dstWriter, "\n")

	for buf.Len() != 0 {
		b, _ := (buf).ReadByte()
		if b&0x80 != 0 {
			//fmt.Fprintf(dstWriter, "Copy instruction\n")
			copyOfsFlags := (b & git.CopyOffsetFlagsMask)
			copySizeFlags := (b & git.CopySizeFlagsMask) >> git.CopySizeFlagsShift
			ofs := readDeltaCopyOffset(copyOfsFlags, buf)
			size := readDeltaCopySize(copySizeFlags, buf)
			////fmt.Fprintf(dstWriter, "The copy header byte is: %b\n", b)
			////fmt.Fprintf(dstWriter, "The copy size is: %d\n", size)
			////fmt.Fprintf(dstWriter, "The copy offset is: %d\n", ofs)
			////fmt.Fprintf(dstWriter, "\n")
			ops = append(ops, CopyOp{Offset: ofs, Size: size})
		} else {
			// fmt.Fprintf(dstWriter, "Insert instruction\n")
			payloadSize := (b & git.InsertSizeMask)
			//fmt.Fprintf(dstWriter, "The insert header byte is: %b\n", b)
			// fmt.Fprintf(dstWriter, "The insert size is: %d\n", payloadSize)
			insertPayloadBuf := make([]byte, payloadSize)
			io.ReadFull(buf, insertPayloadBuf)
			////fmt.Fprintf(dstWriter, "The insert payload is: %s\n", insertPayloadBuf)
			////fmt.Fprintf(dstWriter, "\n")
			ops = append(ops, InsertOp{PayloadSize: payloadSize, Payload: insertPayloadBuf})
		}
	}
	//io.Copy(dstWriter, buf)
	//fmt.Fprintf(dstWriter, "\n")
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
	for i := range git.CopyOffsetFlagsLen {
		if (0b00000001 & (ofsFlags >> i)) == 1 {
			b, _ := br.ReadByte()
			ofs |= uint64(b) << (8 * i)
		}
	}
	return ofs
}

func readDeltaCopySize(sizeFlags byte, br *bytes.Buffer) (size uint64) {
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
