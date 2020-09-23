package main

import (
	"crypto/md5"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

// Calculate the MD5 sum of a single file
func MD5SumFile(path string) (sum string, err error) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	h := md5.New()
	_, err = io.Copy(h, f)
	if err != nil {
		return
	}
	sum = fmt.Sprintf("%x", h.Sum(nil))
	return
}

// walkFiles starts a goroutine to walk the directory tree at root and send the
// path of each regular file on the string channel.  It sends the result of the
// walk on the error channel.  If done is closed, walkFiles abandons its work.
func walkFiles(done <-chan struct{}, root string) (<-chan string, <-chan error) {
	paths := make(chan string)
	errc := make(chan error, 1)
	go func() { // HL
		// Close the paths channel after Walk returns.
		defer close(paths) // HL
		// No select needed for this send, since errc is buffered.
		errc <- filepath.Walk(root, func(path string, info os.FileInfo, err error) error { // HL
			if err != nil {
				return err
			}
			if !info.Mode().IsRegular() {
				return nil
			}
			select {
			case paths <- path: // HL
			case <-done: // HL
				return errors.New("walk canceled")
			}
			return nil
		})
	}()
	return paths, errc
}

// A result is the product of reading and summing a file using MD5.
type result struct {
	path string
	sum  []byte
	err  error
}

// digester reads path names from paths and sends digests of the corresponding
// files on c until either paths or done is closed.
func digester(done <-chan struct{}, paths <-chan string, c chan<- result) {
	for path := range paths {
		var err error
		h := md5.New()
		f, err := os.Open(path)
		if err == nil {
			_, err = io.Copy(h, f)
		}
		defer f.Close()

		select {
		case c <- result{path, h.Sum(nil), err}:
		case <-done:
			return
		}
	}
}

// md5All reads all the files in the file tree rooted at root and returns a map
// from file path to the MD5 sum of the file's contents.  If the directory walk
// fails or any read operation fails, md5All returns an error.  In that case,
// md5All does not wait for inflight read operations to complete.
func md5All(root string) (map[string][]byte, error) {
	// md5All closes the done channel when it returns; it may do so before
	// receiving all the values from c and errc.
	done := make(chan struct{})
	defer close(done)

	paths, errc := walkFiles(done, root)

	// Start a fixed number of goroutines to read and digest files.
	c := make(chan result) // HLc
	var wg sync.WaitGroup
	const numDigesters = 20
	wg.Add(numDigesters)
	for i := 0; i < numDigesters; i++ {
		go func() {
			digester(done, paths, c) // HLc
			wg.Done()
		}()
	}
	go func() {
		wg.Wait()
		close(c) // HLc
	}()
	// End of pipeline. OMIT

	m := make(map[string][]byte)
	for r := range c {
		if r.err != nil {
			return nil, r.err
		}
		m[r.path] = r.sum
	}
	// Check whether the Walk failed.
	if err := <-errc; err != nil { // HLerrc
		return nil, err
	}
	return m, nil
}

// Calculate the MD5 sum of a whole directory
func MD5SumDir(path string) (sum string, err error) {
	ms, err := md5All(path)
	if err != nil {
		return
	}
	var paths []string
	for path := range ms {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	h := md5.New()
	var s []byte
	for _, path := range paths {
		io.WriteString(h, path)
		s = ms[path]
		h.Write(s)
	}
	sum = fmt.Sprintf("%x", h.Sum(nil))
	return
}
