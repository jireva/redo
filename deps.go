package main

import (
	"bufio"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// A node in the dependency graph
type Node struct {
	// Filesystem attributes of the node
	Dir, File, DoScript                    string
	IsTarget, Exists, IsDir, UsesDefaultDo bool
	ModTime                                time.Time

	// internal state
	lockFile *os.File
}

// Takes a path to a node and returns a *Node
func NewNode(path string) (n *Node, err error) {
	n = new(Node)
	n.Dir, n.File = filepath.Split(path)
	var s os.FileInfo
	if s, err = os.Stat(path); err == nil {
		n.Exists = true
		n.IsDir = s.IsDir()
		n.ModTime = s.ModTime()
		if s, err = os.Stat(path + ".prereqs"); err == nil {
			n.IsTarget = true
		} else if os.IsNotExist(err) {
			err = nil
			return
		} else {
			return
		}
	} else if os.IsNotExist(err) {
		n.IsTarget = true
		err = nil
	} else {
		return
	}
	if s, err = os.Stat(path + ".do"); err == nil {
		n.DoScript = n.File + ".do"
	} else if os.IsNotExist(err) {
		ext := filepath.Ext(n.File)
		if s, err = os.Stat(n.Dir + "default" + ext + ".do"); err == nil {
			n.UsesDefaultDo = true
			n.DoScript = "default" + ext + ".do"
		} else if os.IsNotExist(err) {
			err = fmt.Errorf("do script is missing but prereqs exists: %s", path)
			return
		} else {
			return
		}
	} else {
		return
	}
	return
}

// Walk through the dependency tree and update deps as necessary
// Returns true if changed
func (n *Node) RedoIfChange() (changed bool, err error) {
	if !n.IsTarget {
		return
	}
	if !n.Exists {
		return true, n.Build()
	}
	f, err := os.Open(n.Dir + n.File + ".prereqs")
	if err != nil {
		return
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	var line []string
	var o *Node
	var t int64
	var hashChanged bool
	var wg sync.WaitGroup
	for scanner.Scan() {
		line = strings.Split(scanner.Text(), "	")
		if line[1] == "ifcreate" {
			if _, err = os.Stat(n.Dir + line[0]); err == nil {
				changed = true
				continue
			} else if os.IsNotExist(err) {
				continue
			} else {
				return
			}
		}
		o, err = NewNode(n.Dir + line[0])
		if err != nil {
			return
		}
		if !o.IsTarget {
			t, err = strconv.ParseInt(line[1], 10, 64)
			if err != nil {
				return
			}
			hashChanged, err = o.HashChanged(time.Unix(t, 0), line[2])
			if err != nil {
				return
			}
			changed = changed || hashChanged
			continue
		}
		if !o.Exists {
			changed = true
			o.Build()
			continue
		}
		t, err = strconv.ParseInt(line[1], 10, 64)
		if err != nil {
			return
		}
		hashChanged, err = o.HashChanged(time.Unix(t, 0), line[2])
		if err != nil {
			return
		}
		if hashChanged {
			err = fmt.Errorf("hash changed since last build")
			return
		}
		wg.Add(1)
		go func(n *Node) {
			defer wg.Done()
			_, err = n.RedoIfChange()
			if err != nil {
				log.Fatalln(fmt.Errorf("while building %s: %v", n.Dir+n.File, err))
			}
		}(o)
	}
	wg.Wait()
	if err = scanner.Err(); err != nil {
		return
	}
	if changed {
		n.Build()
	}
	return
}

// Check if hash has changed since last build
func (n *Node) HashChanged(lastModTime time.Time, lastHash string) (bool, error) {
	if n.ModTime.Equal(lastModTime) {
		return true, nil
	}
	h, err := n.Hash()
	if err != nil {
		return false, err
	}
	return h != lastHash, nil
}

func (n *Node) Hash() (string, error) {
	if n.IsDir {
		return MD5SumDir(n.Dir + n.File)
	} else {
		return MD5SumFile(n.Dir + n.File)
	}
}

// Run n.DoScript to build target
func (n *Node) Build() (err error) {
	done, err := n.Lock()
	if done || err != nil {
		return
	}
	defer n.UnLock()
	fmt.Fprintln(os.Stderr, "redo", n.Dir+n.File)

	prereqsFile, err := os.Create(n.Dir + n.File + ".prereqs")
	if err != nil {
		return fmt.Errorf("could not create prereqs file: %v", err)
	}
	defer prereqsFile.Close()

	do, err := NewNode(n.Dir + n.DoScript)
	if err != nil {
		return fmt.Errorf("unable to open do script: %v", err)
	}
	h, err := do.Hash()
	if err != nil {
		return fmt.Errorf("unable to hash do exec: %v", err)
	}
	_, err = fmt.Fprintf(prereqsFile, "%s	%d	%s\n",
		do.File,
		do.ModTime.Unix(),
		h)
	if err != nil {
		return
	}
	if n.UsesDefaultDo {
		_, err = fmt.Fprintf(prereqsFile, "%s	ifcreate\n", n.File+".do")
		if err != nil {
			log.Fatalln(fmt.Errorf("unable to add ifcreate dep for non-default do: %v", err))
		}
	}

	// Set RedoParentEnv
	parent, err := filepath.Abs(n.Dir + n.File)
	if err != nil {
		return
	}
	env := os.Environ()
	var inserted bool
	for i, e := range env {
		if strings.HasPrefix(e, RedoParentEnv) {
			env[i] = RedoParentEnv + "=" + parent
			inserted = true
		}
	}
	if !inserted {
		env = append(env, RedoParentEnv+"="+parent)
	}

	tmpStdout := n.Dir + "redo-stdout---" + n.File
	redoArg3 := "redo-redoArg3---" + n.File
	tmpStdoutFile, err := os.Create(tmpStdout)
	if err != nil {
		return
	}
	defer tmpStdoutFile.Close()
	defer os.Remove(tmpStdout)

	c := exec.Command("./"+n.DoScript, n.File, "", redoArg3)
	c.Dir = n.Dir
	c.Stdout = tmpStdoutFile
	c.Stderr = os.Stderr
	c.Env = env
	if err = c.Run(); err != nil {
		return fmt.Errorf("failed while rebuilding: %v", err)
	}

	stdoutStat, err := os.Stat(tmpStdout)
	if err != nil {
		return
	}
	stdoutSize := stdoutStat.Size()

	arg3 := false
	if _, err = os.Stat(n.Dir + redoArg3); err == nil {
		arg3 = true
	} else if os.IsNotExist(err) {
		err = nil
	} else {
		return
	}

	if arg3 && stdoutSize > 0 {
		return fmt.Errorf("do program wrote to stdout and to $3")
	}
	if stdoutSize > 0 {
		if err = tmpStdoutFile.Sync(); err != nil {
			return
		}
		return os.Rename(tmpStdout, n.Dir+n.File)
	}
	if arg3 {
		if n.Exists && n.IsDir {
			if err = os.RemoveAll(n.Dir + n.File); err != nil {
				return
			}
		}
		err = filepath.Walk(n.Dir+redoArg3, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			file, err := os.Open(path)
			defer file.Close()
			if err != nil {
				return err
			}
			if err = file.Sync(); err != nil {
				return err
			}
			return nil
		})
		if err != nil {
			return
		}
		err = os.Rename(n.Dir+redoArg3, n.Dir+n.File)
	}
	return
}

const (
	stateDeps     = "deps"
	stateBuilding = "building"
)

func (n *Node) Lock() (done bool, err error) {
	if _, err = os.Stat(n.Dir + n.File + ".lock"); err == nil {
		for {
			time.Sleep(time.Second)
			if _, err = os.Stat(n.Dir + n.File + ".lock"); os.IsNotExist(err) {
				return true, nil
			}
			log.Printf("waiting for %s...\n", n.Dir+n.File)
		}
	} else if os.IsNotExist(err) {
		var prereqsStat os.FileInfo
		if prereqsStat, err = os.Stat(n.Dir + n.File + ".prereqs"); err == nil {
			if prereqsStat.ModTime().After(RedoTreeTime) {
				return true, nil
			}
		} else if !os.IsNotExist(err) {
			return
		}
		n.lockFile, err = os.Create(n.Dir + n.File + ".lock")
		if err != nil {
			return false, fmt.Errorf("could not create lock file: %v", err)
		}
	}
	return
}

func (n *Node) UnLock() (err error) {
	err = n.lockFile.Close()
	if err != nil {
		return
	}
	return os.Remove(n.Dir + n.File + ".lock")
}

func (n *Node) AddDep(prereqsFile *os.File) (err error) {
	h, err := n.Hash()
	if err != nil {
		return fmt.Errorf("unable to hash: %v", err)
	}
	_, err = fmt.Fprintf(prereqsFile, "%s	%d	%s\n",
		n.Dir+n.File,
		n.ModTime.Unix(),
		h)
	return
}
