package main

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// A node in the dependency graph
type Node struct {
	// Filesystem attributes of the node
	Dir, File, DoScript                    string
	IsTarget, Exists, IsDir, UsesDefaultDo bool

	// internal state
	lockFile *os.File
}

// Takes a path to a node and returns a *Node
func NewNode(path string) (n *Node, err error) {
	n = &Node{}
	n.Dir, n.File = filepath.Split(path)
	var s os.FileInfo

	if s, err = os.Stat(path + ".prereqs"); err == nil {
		n.IsTarget = true
	} else if os.IsNotExist(err) {
		err = nil
	} else {
		return
	}

	if s, err = os.Stat(path); err == nil {
		n.Exists = true
		n.IsDir = s.IsDir()
	} else if os.IsNotExist(err) {
		err = nil
	} else {
		return
	}

	if s, err = os.Stat(path + ".do"); err == nil {
		n.IsTarget = true
		n.DoScript = n.File + ".do"
	} else if os.IsNotExist(err) {
		ext := filepath.Ext(n.File)
		if s, err = os.Stat(n.Dir + "default" + ext + ".do"); err == nil {
			n.UsesDefaultDo = true
			n.DoScript = "default" + ext + ".do"
			n.IsTarget = true
		} else if os.IsNotExist(err) {
			err = nil
		} else {
			return
		}
	} else {
		return
	}

	if n.IsTarget && n.DoScript == "" {
		err = fmt.Errorf("file %s has .prereqs but no do exec", path)
		return
	}
	return
}

// Walk through the dependency tree and update deps as necessary
// Returns true if changed
func (n *Node) RedoIfChange(ctx context.Context, cancelCause context.CancelCauseFunc) (changed bool, err error) {
	select {
	case <-ctx.Done():
		return false, context.Cause(ctx)
	default:
	}

	if !n.IsTarget {
		return
	}
	if !n.Exists {
		return true, n.build(ctx)
	}
	f, err := os.Open(n.Dir + n.File + ".prereqs")
	if err != nil {
		return
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	var line []string
	var o *Node
	var hashChanged bool
	var wg sync.WaitGroup
	for scanner.Scan() {
		line = strings.Split(scanner.Text(), "	")
		if line[1] == "ifcreate" {
			o, err = NewNode(n.Dir + line[0])
			if err != nil {
				return
			}
			created, err := o.RedoIfCreate()
			if err != nil {
				return false, err
			}
			if created {
				changed = true
			}
			continue
		}
		if line[1] != "ifchange" {
			err = fmt.Errorf("Unknown dependency type: %s", line[1])
			return
		}
		o, err = NewNode(n.Dir + line[0])
		if err != nil {
			return
		}
		if !o.IsTarget {
			hashChanged, err = o.HashChanged(line[2])
			if err != nil {
				return
			}
			changed = changed || hashChanged
			continue
		}
		if !o.Exists {
			changed = true
			if err = o.build(ctx); err != nil {
				return
			}
			continue
		}
		hashChanged, err = o.HashChanged(line[2])
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
			_, err = n.RedoIfChange(ctx, cancelCause)
			if err != nil {
				cancelCause(err)
				return
			}
		}(o)
	}
	wg.Wait()
	if err = scanner.Err(); err != nil {
		return
	}
	if changed {
		err = n.build(ctx)
	}
	return
}

// Checks if file has been created
// Returns true if it has been created
func (n *Node) RedoIfCreate() (bool, error) {
	if _, err := os.Stat(n.Dir + n.File); err == nil {
		log.Printf("%s created since last run\n", n.Dir+n.File)
		return true, nil
	} else if os.IsNotExist(err) {
		return false, nil
	} else {
		return false, err
	}
}

func (n *Node) StopIfChange() (err error) {
	if !n.Exists {
		err = fmt.Errorf("file does not exist")
		return
	}
	new_hash, err := n.Hash()
	if _, err = os.Stat(n.Dir + n.File + ".md5"); err == nil {
		old_hash, err := os.ReadFile(n.Dir + n.File + ".md5")
		if err != nil {
			return err
		}
		if strings.HasPrefix(string(old_hash), new_hash) {
			return nil
		} else {
			err = fmt.Errorf("Hash changed since last build: %s", n.Dir+n.File)
			return err
		}
	} else if os.IsNotExist(err) {
		log.Printf("hashing \"%s\" for the first time, integrity will be preserved hereafter.\n", n.Dir+n.File)
		md5File, err := os.Create(n.Dir + n.File + ".md5")
		if err != nil {
			return fmt.Errorf("unable to write hash for %s:", n.Dir+n.File, err)
		}
		defer md5File.Close()
		_, err = fmt.Fprintf(md5File, "%s	%s\n", new_hash, n.File)
		if err != nil {
			return fmt.Errorf("unable to write hash for %s:", n.Dir+n.File, err)
		}
		return nil
	} else {
		return err
	}
}

// Check if hash has changed since last build
func (n *Node) HashChanged(lastHash string) (bool, error) {
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
func (n *Node) build(ctx context.Context) (err error) {
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
	_, err = fmt.Fprintf(prereqsFile, "%s	ifchange	%s\n",
		do.File,
		h)
	if err != nil {
		return
	}
	if n.UsesDefaultDo {
		_, err = fmt.Fprintf(prereqsFile, "%s	ifcreate\n", n.File+".do")
		if err != nil {
			err = fmt.Errorf("unable to add ifcreate dep for non-default do: %v", err)
			return
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

	c := exec.CommandContext(
		ctx,
		"./"+n.DoScript,
		n.File,
		n.File[:len(n.File)-len(filepath.Ext(n.File))],
		redoArg3)
	c.Dir = n.Dir
	c.Stdout = tmpStdoutFile
	c.Stderr = os.Stderr
	c.Env = env
	c.Cancel = func() error {
		return c.Process.Signal(os.Interrupt)
	}
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
			if err != nil {
				return err
			}
			defer file.Close()
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
	_, err = fmt.Fprintf(prereqsFile, "%s	ifchange	%s\n",
		n.Dir+n.File,
		h)
	return
}
