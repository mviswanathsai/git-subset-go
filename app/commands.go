package main

import (
	"bufio"
	"bytes"
	"compress/zlib"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	fp "path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/codecrafters-io/git-starter-go/internal/files"
	"github.com/codecrafters-io/git-starter-go/internal/git"
	"github.com/codecrafters-io/git-starter-go/internal/hashes"
)

type Command struct {
	Name string
	Run  func(args []string) error
}

var commands = map[string]Command{
	"init":        {Name: "init", Run: handleInit},
	"cat-file":    {Name: "cat-file", Run: handleCatFile},
	"hash-object": {Name: "hash-object", Run: handleHashObject},
	"write-tree":  {Name: "write-tree", Run: handleWriteTree},
	"commit-tree": {Name: "commit-tree", Run: handleCommitTree},
	"verify-tree": {Name: "verify-tree", Run: handleVerifyTree},
	"ls-tree":     {Name: "ls-tree", Run: handleLsTree},
	"ls-remote":   {Name: "ls-remote", Run: handleLsRemote},
	"clone":       {Name: "clone", Run: handleClone},
}

func handleInit(args []string) error {
	for _, dir := range []string{".git", ".git/objects", ".git/refs"} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("Error creating directory: %s\n", err)
		}
	}

	headFileContents := []byte("ref: refs/heads/main\n")
	if err := os.WriteFile(".git/HEAD", headFileContents, 0644); err != nil {
		return fmt.Errorf("Error writing file: %s\n", err)
	}

	fmt.Println("Initialized git directory")
	return nil

}

func handleCatFile(args []string) error {
	fs := flag.NewFlagSet("cat-file", flag.ExitOnError)
	fs.Bool("p", false, "Pretty-print the contents of <object> based on its type")

	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return fmt.Errorf("missing object hash")
	}
	objhash := fs.Arg(0)

	objDirName, objFileName := hashes.DecomposeHash(objhash)

	// we first need to open the file
	filePath := fp.Join(git.GitObjDir, objDirName, objFileName)
	f, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("Error opening file: %v\n", err)
	}

	zr, err := zlib.NewReader(f)
	if err != nil {
		return fmt.Errorf("Error decompressing file: %v\n", err)
	}
	defer zr.Close()

	r := bufio.NewReader(zr)
	if _, err := r.ReadBytes(git.ObjHeaderDelim); err != nil {
		return fmt.Errorf("Error reading object file: %v\n", err)
	}
	if _, err := io.Copy(os.Stdout, r); err != nil {
		return fmt.Errorf("Error writing to stdout: %v\n", err)
	}
	return nil
}

func handleHashObject(args []string) error {
	fs := flag.NewFlagSet("hash-object", flag.ExitOnError)
	write := fs.Bool("w", false, "write the object to the object database")
	if fs.NArg() < 1 {
		return fmt.Errorf("file path required")
	}
	filePath := fs.Arg(0)
	fs.Parse(args)

	f, finfo, err := files.OpenFile(filePath)
	if err != nil {
		return fmt.Errorf("Error opening object file: %v", err)
	}

	h, err := hashes.HashAndWriteObject(f, finfo.Size(), "blob", *write)
	if err != nil {
		return fmt.Errorf("Error hashing/writing object: %v", err)
	}

	// Print the hex encoding of the hash.
	fmt.Print(h)
	return nil
}

func handleLsTree(args []string) error {
	fs := flag.NewFlagSet("ls-tree", flag.ExitOnError)
	nameOnly := fs.Bool("name-only", false, "List only filenames")

	// 2. Parse arguments (handles both: --name-only <hash> and <hash> --name-only)
	if err := fs.Parse(args); err != nil {
		return err
	}

	// 3. The first non-flag argument is our tree hash
	if fs.NArg() < 1 {
		return fmt.Errorf("missing tree hash")
	}
	treeh := fs.Arg(0)

	// given a hash, read the file and output
	objDirName, objFileName := hashes.DecomposeHash(treeh)
	filepath := fp.Join(git.GitObjDir, objDirName, objFileName)
	f, err := os.Open(filepath)
	if err != nil {
		return fmt.Errorf("Error opening file: %v\n", err)
	}

	zr, err := zlib.NewReader(f)
	if err != nil {
		return fmt.Errorf("Error reading file: %v\n", err)
	}

	b, err := io.ReadAll(zr)
	if err != nil {
		return fmt.Errorf("Error reading file: %v\n", err)
	}

	// Trim the tree header
	delimIdx := bytes.IndexByte(b, '\x00')
	b = b[delimIdx+1:]

	out, err := parseTreeObject(b, *nameOnly)
	if err != nil {
		return fmt.Errorf("Error parsing tree: %v", err)
	}

	slices.Sort(out)
	fmt.Printf("%v\n", strings.Join(out, "\n"))
	return nil
}

func handleWriteTree(args []string) error {
	ex, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("Error getting current working dir: %v\n", err)
	}

	cwd, err := os.ReadDir(ex)
	if err != nil {
		return fmt.Errorf("Error reading current working dir: %v\n", err)
	}

	hash := sha1.New()
	zw := zlib.NewWriter(nil)

	treeBuilder := &TreeBuilder{
		hash: hash,
		zw:   zw,
	}

	payload, err := treeBuilder.generateTreePayload(cwd, ".")
	if err != nil {
		return fmt.Errorf("Error generating tree payload: %v", err)
	}

	tmpf := files.CreateTempObjFile()
	defer os.Remove(tmpf.Name())

	hash.Reset()
	zw.Reset(tmpf)
	mw := io.MultiWriter(hash, zw)

	if err := files.WriteGitObject(mw, "tree", payload.Len(), payload); err != nil {
		return fmt.Errorf("Error writing object to disk: %v", err)
	}
	h := hex.EncodeToString(hash.Sum(nil))

	objDirName, objFileName := hashes.DecomposeHash(h)
	err = os.MkdirAll(fp.Join(git.GitObjDir, objDirName), 0775)
	if err != nil && !errors.Is(err, fs.ErrExist) {
		return fmt.Errorf("Error creating dir: %v\n", err)
	}

	objFilePath := fp.Join(git.GitObjDir, objDirName, objFileName)
	if os.Rename(tmpf.Name(), objFilePath) != nil {
		return fmt.Errorf("Error creating blob object: %v\n", err)
	}

	if err := zw.Close(); err != nil {
		return fmt.Errorf("Error closing zlib writer: %v\n", err)
	}

	fmt.Println(h)
	return nil
}

func handleCommitTree(args []string) error {
	flagset := flag.NewFlagSet("commit-tree", flag.ExitOnError)
	parentSha := flagset.String("p", "", "ID of the parent commit object")
	message := flagset.String("m", "", "Commit message")

	flagset.Parse(args)

	if flagset.NArg() < 1 {
		return fmt.Errorf("missing tree SHA")
	}
	treeSha := flagset.Arg(0)

	author := "Teenus Lorvalds"
	email := "teenus@lorvalds.com"
	t := time.Now()
	timestamp := t.Unix()
	offset := t.Format("-0700")

	buf := new(bytes.Buffer)
	fmt.Fprintf(buf, "tree %s\n", treeSha)
	fmt.Fprintf(buf, "parent %s\n", *parentSha)
	fmt.Fprintf(buf, "author %s <%s> %d %s\n", author, email, timestamp, offset)
	fmt.Fprintf(buf, "\n%s\n", *message)

	tmpf := files.CreateTempObjFile()
	hash := sha1.New()
	zw := zlib.NewWriter(tmpf)
	mw := io.MultiWriter(hash, zw)

	if err := files.WriteGitObject(mw, "commit", buf.Len(), buf); err != nil {
		return fmt.Errorf("Error writing object to disk: %v", err)
	}
	if err := zw.Close(); err != nil {
		return fmt.Errorf("Error writing object to disk: %v", err)
	}
	tmpf.Close()

	h := hex.EncodeToString(hash.Sum(nil))
	objDirName, objFileName := hashes.DecomposeHash(h)
	if err := os.MkdirAll(fp.Join(git.GitObjDir, objDirName), 0775); err != nil && !errors.Is(err, fs.ErrExist) {
		return fmt.Errorf("Error creating dir: %v\n", err)
	}

	objFilePath := fp.Join(git.GitObjDir, objDirName, objFileName)
	if err := os.Rename(tmpf.Name(), objFilePath); err != nil {
		return fmt.Errorf("Error creating commit object: %v\n", err)
	}

	fmt.Println(h)
	return nil
}

func handleVerifyTree(args []string) error {
	fs := flag.NewFlagSet("verify-pack", flag.ExitOnError)
	fs.Parse(args)

	// 2. Use fs.Arg(0) instead of os.Args[2]
	if fs.NArg() < 1 {
		return fmt.Errorf("usage: verify-pack <pack-file-path>")
	}
	pfPath := fs.Arg(0)

	// Open the file instead for comparison
	pf, fileInfo, err := files.OpenFile(pfPath)
	if err != nil {
		return fmt.Errorf("Error opening pack file: %v", err)
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

	if _, err := streamer.VerifyPackTrailer(); err != nil {
		return fmt.Errorf("Invalid Pack: %v", err)
	}

	packOrder, packIndex, err := streamer.ReadPackAndBuildIndex()
	if err != nil {
		return fmt.Errorf("Pack header invalid: %v", err)
	}

	builder := &ObjectBuilder{
		packIndex:   packIndex,
		lookupCache: make(map[uint64]*ResolvedObject),
		packOrder:   packOrder,
		br:          br,
		zr:          streamer.zr,
		file:        pf,
		fileInfo:    fileInfo,
		h:           h,
	}

	stats := &ObjectStats{
		ChainCounts:   make(map[int]int),
		NonDeltaCount: 0,
	}

	if err := builder.ForEachObjectResult(stats.ProcessResult); err != nil {
		return fmt.Errorf("Error processing packfile stats: %v", err)
	}

	stats.PrintSummary()

	fmt.Printf("%s: ok\n", pfPath)
	return nil
}

func handleLsRemote(args []string) error {
	fs := flag.NewFlagSet("ls-remote", flag.ExitOnError)
	fs.Parse(args)

	if fs.NArg() < 1 {
		return fmt.Errorf("usage: ls-remote <repository>")
	}
	repo := fs.Arg(0)

	str := fmt.Sprintf("%s/info/refs?service=git-upload-pack", repo)
	resp, err := http.Get(str)
	if err != nil {
		fmt.Printf("Error fetching refs: %v", err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNotModified {
		return fmt.Errorf("Invalid status received for request: %s\n", resp.Status)
	}

	br := bufio.NewReader(resp.Body)
	if err := validateUploadPackResponse(br); err != nil {
		return fmt.Errorf("Invalid response for git-upload-pack request: %v\n", err)
	}

	if _, err := readPktLine(br); err != nil {
		return fmt.Errorf("Error reading packet line: %v", err)
	}
	for {
		line, err := readPktLine(br)
		if err != nil {
			return fmt.Errorf("Error reading packet line: %v", err)
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
	return nil
}

func handleClone(args []string) error {
	flagset := flag.NewFlagSet("clone", flag.ExitOnError)
	flagset.Parse(args)

	// Validate positional arguments
	if flagset.NArg() < 2 {
		return fmt.Errorf("usage: clone <url> <working-directory>")
	}

	url := flagset.Arg(0)
	workingDir := flagset.Arg(1)

	err := os.MkdirAll(fp.Join(workingDir, git.GitObjDir), 0775)
	if err != nil {
		return fmt.Errorf("Error creating repo dir: %v\n", err)
	}

	if err := os.Chdir(workingDir); err != nil {
		return fmt.Errorf("failed to enter repository directory %q: %v\n", workingDir, err)
	}

	br := bufio.NewReader(nil)

	refs, commonCaps, err := DiscoverRefs(url, br)
	if err != nil {
		return fmt.Errorf("Error discovering refs: %v\n", err)
	}

	res, err := NegotiateAndReturnResponse(url, refs, commonCaps)
	if err != nil {
		return fmt.Errorf("Error negotiating packfile: %v\n", err)
	}

	tmp, err := DemuxNegotiationResponse(br, res)
	if err != nil {
		return fmt.Errorf("Error while demuxing response: %v\n", err)
	}

	fileInfo, err := os.Stat(tmp.Name())
	if err != nil {
		return fmt.Errorf("Error opening packfile: %v", err)
	}

	streamer := &PackStreamer{
		f:     tmp,
		br:    br,
		h:     sha1.New(),
		fInfo: fileInfo,
	}

	checksum, err := streamer.VerifyPackTrailer()
	if err != nil {
		os.Remove(tmp.Name())
		return fmt.Errorf("Invalid packfile received: %v", err)
	}

	err = os.MkdirAll(fp.Join(git.GitObjDir, "pack"), 0775)
	if err != nil && !errors.Is(err, fs.ErrExist) {
		return fmt.Errorf("Error creating pack dir: %v\n", err)
	}

	// Git actually calculates a different hash for the packfile name. But that isn't
	// super important for this demonstration.
	packFile := fp.Join(git.GitObjDir, "pack", fmt.Sprintf("pack-%x.pack", checksum))
	if err := os.Rename(tmp.Name(), packFile); err != nil {
		return fmt.Errorf("Error writing packfile: %v\n", err)
	}

	pf := tmp

	packOrder, packIndex, err := streamer.ReadPackAndBuildIndex()
	if err != nil {
		return fmt.Errorf("Error processing packfile: %v\n", err)
	}

	builder := &ObjectBuilder{
		packIndex:   packIndex,
		lookupCache: make(map[uint64]*ResolvedObject),
		packOrder:   packOrder,
		hashMap:     make(map[string]*ResolvedObject),
		br:          br,
		file:        pf,
		fileInfo:    fileInfo,
		zr:          streamer.zr,
		h:           sha1.New(),
	}

	storer := NewObjectStorer()

	if err := builder.ForEachObject(storer.Store); err != nil {
		return fmt.Errorf("Error persisting objects in the object store: %v\n", err)
	}
	// Get rid of the cache at this point, we don't need it anymore
	// Doesn't change much in terms of performance.
	builder.ClearCache()

	repoBuilder := &RepoBuilder{
		objectSource: storer.hashMap,
		refs:         refs,
	}

	// create the .git/refs directory
	if err := repoBuilder.CreateRefs(); err != nil {
		return fmt.Errorf("Error creating refs: %v\n", err)
	}

	if err := repoBuilder.CreateHeads(); err != nil {
		return fmt.Errorf("Error creating HEADs: %v\n", err)
	}

	// Read it from the hashMap
	checkoutResult, err := repoBuilder.CheckoutHeadCommit()
	if err != nil {
		return fmt.Errorf("Error checking out HEAD commit %s: %v\n", repoBuilder.refs["HEAD"], err)
	}

	if err := repoBuilder.WriteIndex(checkoutResult.indexEntries); err != nil {
		return fmt.Errorf("Error writing index: %v\n", err)
	}
	return nil
}
