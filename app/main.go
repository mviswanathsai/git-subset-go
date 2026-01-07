package main

import (
	"bufio"
	"bytes"
	"compress/zlib"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	fp "path/filepath"
	"slices"
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
		if _, err := r.ReadBytes(git.ObjHeaderDelim); err != nil {
			fmt.Printf("Error reading object file: %v\n", err)
			os.Exit(1)
		}
		if _, err := io.Copy(os.Stdout, r); err != nil {
			fmt.Printf("Error writing to stdout: %v\n", err)
			os.Exit(1)
		}

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

		f, finfo, err := files.OpenFile(filename)
		if err != nil {
			fmt.Printf("Error opening object file: %v", err)
			os.Exit(1)
		}

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

		out, err := parseTreeObject(b, nameonly)
		if err != nil {
			fmt.Printf("Error parsing tree: %v", err)
			os.Exit(1)
		}

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
			os.Exit(1)
		}

		hash := sha1.New()
		zw := zlib.NewWriter(nil)

		treeBuilder := &TreeBuilder{
			hash: hash,
			zw:   zw,
		}

		payload, err := treeBuilder.generateTreePayload(cwd, ".")
		if err != nil {
			fmt.Printf("Error generating tree payload: %v", err)
			os.Exit(1)
		}

		tmpf := files.CreateTempObjFile()
		defer os.Remove(tmpf.Name())

		hash.Reset()
		zw.Reset(tmpf)
		mw := io.MultiWriter(hash, zw)

		if err := files.WriteGitObject(mw, "tree", payload.Len(), payload); err != nil {
			fmt.Printf("Error writing object to disk: %v", err)
			os.Exit(1)
		}
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

		if err := zw.Close(); err != nil {
			fmt.Fprintf(os.Stderr, "Error closing zlib writer: %v\n", err)
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

		if err := files.WriteGitObject(mw, "commit", buf.Len(), buf); err != nil {
			fmt.Printf("Error writing object to disk: %v", err)
			os.Exit(1)
		}
		if err := zw.Close(); err != nil {
			fmt.Printf("Error writing object to disk: %v", err)
			os.Exit(1)
		}
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
		pf, fileInfo, err := files.OpenFile(pfPath)
		if err != nil {
			fmt.Printf("Error opening pack file: %v", err)
			os.Exit(1)
		}

		h := sha1.New()
		br := bufio.NewReader(pf)

		streamer := &PackStreamer{
			f:   pf,
			br:  br,
			h:   h,
			zr:  nil,
			zbr: nil,
		}

		if _, err := streamer.verifyPackTrailer(); err != nil {
			fmt.Fprintf(os.Stderr, "Invalid Pack: %v", err)
			os.Exit(1)
		}

		_, objCount, err := streamer.readPackHeader()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Pack header invalid: %v", err)
			os.Exit(1)
		}

		packOrder, packIndex, err := streamer.BuildPackIndex(objCount)
		if err != nil {
			fmt.Printf("Error building pack index: %v", err)
			os.Exit(1)
		}

		builder := &objectBuilder{
			packIndex:   packIndex,
			lookupCache: make(map[uint64]*ResolvedObject),
			packOrder:   packOrder,
			br:          br,
			file:        pf,
			fileInfo:    fileInfo,
			hasher:      h,
		}

		stats := &ObjectStats{
			ChainCounts:   make(map[int]int),
			NonDeltaCount: 0,
		}

		if err := builder.ForEachObjectResult(stats.ProcessResult); err != nil {
			fmt.Printf("Error processing packfile stats: %v", err)
			os.Exit(1)
		}

		stats.PrintSummary()

		fmt.Printf("%s: ok\n", pfPath)

	case "ls-remote":
		// just get the references from a remote repo
		repo := os.Args[2]
		str := fmt.Sprintf("%s/info/refs?service=git-upload-pack", repo)
		resp, err := http.Get(str)
		if err != nil {
			fmt.Printf("Error fetching refs: %v", err)
			os.Exit(1)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNotModified {
			os.Exit(1)
		}

		br := bufio.NewReader(resp.Body)
		if err := validateUploadPackResponse(br); err != nil {
			os.Exit(1)
		}

		if _, err := readPktLine(br); err != nil {
			fmt.Printf("Error reading packet line: %v", err)
			os.Exit(1)
		}
		for {
			line, err := readPktLine(br)
			if err != nil {
				fmt.Printf("Error reading packet line: %v", err)
				os.Exit(1)
			} else if line == nil {
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
		url := os.Args[2]
		workingDir := os.Args[3]

		br := bufio.NewReader(nil)

		refs, commonCaps, err := DiscoverRefs(url, br)
		if err != nil {
			fmt.Printf("Error discovering refs: %v\n", err)
			os.Exit(1)
		}

		res, err := NegotiateAndReturnResponse(url, refs, commonCaps)
		if err != nil {
			fmt.Printf("Error negotiating packfile: %v\n", err)
			os.Exit(1)
		}

		tmp, err := DemuxNegotiationResponse(br, res)
		if err != nil {
			fmt.Printf("Error while demuxing response: %v\n", err)
			os.Exit(1)
		}

		// create the .git directory and packfile
		fileInfo, err := os.Stat(tmp.Name())
		if err != nil {
			fmt.Printf("Error opening packfile: %v", err)
			os.Exit(1)
		}

		streamer := &PackStreamer{
			f:  tmp,
			br: br,
			h:  sha1.New(),
		}

		checksum, err := streamer.verifyPackTrailer()
		if err != nil {
			fmt.Printf("Invalid packfile received: %v", err)
			os.Exit(1)
		}

		err = os.MkdirAll(fp.Join(workingDir, git.GitObjDir, "pack"), 0775)
		if err != nil && !errors.Is(err, fs.ErrExist) {
			fmt.Fprintf(os.Stderr, "Error creating pack dir: %v\n", err)
			os.Exit(1)
		}

		// Git actually calculates a different hash for the packfile name. But that isn't
		// super important for this demonstration.
		packFile := fp.Join(git.GitObjDir, "pack", fmt.Sprintf("pack-%x.pack", checksum))
		if err := os.Rename(tmp.Name(), fp.Join(workingDir, packFile)); err != nil {
			fmt.Printf("Error writing packfile: %v\n", err)
		}

		if err := os.Chdir(workingDir); err != nil {
			fmt.Printf("failed to enter repository directory %q: %v\n", workingDir, err)
			os.Exit(1)
		}

		_, objCount, err := streamer.readPackHeader()
		if err != nil {
			fmt.Printf("Pack header invalid: %v\n", err)
			os.Exit(1)
		}

		packOrder, packIndex, err := streamer.BuildPackIndex(objCount)
		if err != nil {
			fmt.Printf("Error building pack index: %v\n", err)
			os.Exit(1)
		}

		var zr io.ReadCloser

		builder := &objectBuilder{
			packIndex:   packIndex,
			lookupCache: make(map[uint64]*ResolvedObject),
			packOrder:   packOrder,
			hashMap:     make(map[string]*ResolvedObject),
			packLength:  objCount,
			br:          br,
			file:        pf,
			fileInfo:    fileInfo,
			zr:          zr,
			hasher:      sha1.New(),
		}

		storer := NewObjectStorer()

		if err := builder.ForEachObject(storer.Store); err != nil {
			fmt.Printf("Error persisting objects in the object store: %v\n", err)
			os.Exit(1)
		}
		// Get rid of the cache at this point, we don't need it anymore
		builder.ClearCache()

		repoBuilder := &RepoBuilder{
			objectSource: storer.hashMap,
			refs:         refs,
		}

		// create the .git/refs directory
		if err := repoBuilder.CreateRefs(); err != nil {
			fmt.Printf("Error creating refs: %v\n", err)
			os.Exit(1)
		}

		if err := repoBuilder.CreateHeads(); err != nil {
			fmt.Printf("Error creating HEADs: %v\n", err)
			os.Exit(1)
		}

		// Read it from the hashMap
		checkoutResult, err := repoBuilder.CheckoutHeadCommit()
		if err != nil {
			fmt.Printf("Error checking out HEAD commit %s: %v\n", repoBuilder.refs["HEAD"], err)
		}

		if err := repoBuilder.WriteIndex(checkoutResult.indexEntries); err != nil {
			fmt.Printf("Error writing index: %v\n", err)
			os.Exit(1)
		}

	default:
		fmt.Fprintf(os.Stderr, "Unknown command %s\n", command)
		os.Exit(1)
	}
}

type checkoutResult struct {
	indexEntries []*IndexEntry
}

func returnCommitTreeSHA(commitData []byte) string {
	return string(commitData[5:45])
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
