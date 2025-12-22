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
	"os"
	fp "path/filepath"
	"slices"
	"strings"
)

const (
	gitObjDir      = ".git/objects"
	objheaderdelim = 0
	xmode          = fs.FileMode(0111)
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

		df, info := returnFile(filename)

		header := getBlobObjHeader(info.Size())

		hash := sha1.New()
		zw := zlib.NewWriter(tmpw)
		mw := io.MultiWriter(hash, zw)

		mw.Write([]byte(header))
		_, err := io.Copy(mw, df)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error hashing/compressing object: %v\n", err)
			os.Exit(1)
		}
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

		objDirName, objFileName := pathFromHash(h)
		err = os.Mkdir(fp.Join(gitObjDir, objDirName), 0775)
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
		objDirName, objFileName := pathFromHash(treeh)
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

		out := generateTreeEntries(b, nameonly)
		slices.Sort(out)
		fmt.Printf("%v\n", strings.Join(out, "\n"))

	case "write-tree":
		// Get the current working directory
		ex, err := os.Getwd()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error getting current working dir: %v\n", err)
		}
		fmt.Println("The current working directory: ", ex)

		cwdFS := os.DirFS(ex)

		var treeEntries []string

		// Walk the current working directory
		fs.WalkDir(cwdFS, ".", func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error walking current working dir: %v\n", err)
				os.Exit(1)
			}
			str, err := generateTreeObject(path, d, treeEntries)
			if err != nil {
				return err
			}

			treeEntries = append(treeEntries, str)
			return nil
		})
		out := strings.Join(treeEntries, "")
		fmt.Print("Out: ", out)

	default:
		fmt.Fprintf(os.Stderr, "Unknown command %s\n", command)
		os.Exit(1)
	}
}

func pathFromHash(hash string) (dirName, objName string) {
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

func returnFile(filename string) (*os.File, fs.FileInfo) {
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

func getBlobObjHeader(fsize int64) string {
	return fmt.Sprintf("blob %d\x00", fsize)
}

func generateTreeEntries(b []byte, nameonly bool) []string {
	var out []string
	for {
		spaceIdx := bytes.IndexByte(b, ' ')
		delimIdx := bytes.IndexByte(b, '\x00')

		if spaceIdx == -1 || delimIdx == -1 {
			fmt.Fprintf(os.Stderr, "Unexpected absence of delim tokens")
		}

		modei := string(b[:spaceIdx])
		namei := string(b[spaceIdx+1 : delimIdx])
		hashi := hex.EncodeToString(b[delimIdx+1 : delimIdx+21])

		if len(modei) == 5 {
			modei = "0" + modei
		}

		if nameonly {
			out = append(out, namei)
		} else {
			out = append(out, fmt.Sprintf("%s %s %s", modei, hashi, namei))
		}

		if len(b[delimIdx+1:]) == 20 {
			break
		}

		b = b[delimIdx+21:]
	}
	return out
}

func generateTreeObject(path string, d fs.DirEntry, treeEntries []string) (string, error) {
	if d.IsDir() && path != "." {
		return "", fs.SkipDir
	} else if !d.IsDir() {
		// For files, we need to create the blob, same as we have done in the past.
		tmpf := createTempObjFile()
		os.Remove(tmpf.Name())

		df, dfinfo := returnFile(d.Name())

		header := getBlobObjHeader(dfinfo.Size())

		hash := sha1.New()
		zw := zlib.NewWriter(tmpf)
		mw := io.MultiWriter(hash, zw)

		mw.Write([]byte(header))
		_, err := io.Copy(mw, df)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error hashing/compressing object: %v\n", err)
			os.Exit(1)
		}
		hsum := hash.Sum(nil)
		//		h := hex.EncodeToString(hsum)
		zw.Close()
		tmpf.Close()

		////	objDirName, objFileName := pathFromHash(h)
		////	err = os.Mkdir(fp.Join(gitObjDir, objDirName), 0775)
		////	if err != nil && !errors.Is(err, fs.ErrExist) {
		////		fmt.Fprintf(os.Stderr, "Error creating dir: %v\n", err)
		////		os.Exit(1)
		////	}

		////	objFilePath := fp.Join(gitObjDir, objDirName, objFileName)
		////	if os.Rename(tmpf.Name(), objFilePath) != nil {
		////		fmt.Fprintf(os.Stderr, "Error creating blob object: %v\n", err)
		////		os.Exit(1)
		////	}

		info, err := d.Info()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error writing tree: %v\n", err)
			os.Exit(1)
		}

		dmode := info.Mode().Perm()
		var gitMode string
		if dmode&xmode == 0 {
			gitMode = "100644"
		} else {
			gitMode = "100755"
		}

		return fmt.Sprintf("%s %s\x00%s", gitMode, d.Name(), hsum), nil
	}
	return "", nil
}
