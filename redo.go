// redo - build utility
package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"
)

var (
	RedoParentEnv   = "REDOPARENT"
	RedoTreeTimeEnv = "REDOTREETIME"
	RedoTreeTime    time.Time
)

func main() {
	log.SetFlags(0)
	progName := filepath.Base(os.Args[0])
	log.SetPrefix("While " + progName + "ing ")

	t := os.Getenv(RedoTreeTimeEnv)
	if t == "" {
		RedoTreeTime = time.Now()
		os.Setenv(RedoTreeTimeEnv, strconv.FormatInt(RedoTreeTime.Unix(), 10))
	} else {
		t, err := strconv.ParseInt(t, 10, 64)
		if err != nil {
			log.Fatalln("Unable to parse", RedoTreeTimeEnv, t)
		}
		RedoTreeTime = time.Unix(t, 0)
	}

	if progName != "redo-ifchange" && progName != "redo" && progName != "redo-ifcreate" && progName != "redo-unless-change" {
		log.Fatalln("Unrecognized executable name:", progName)
	}

	var err error
	var n *Node
	var wg sync.WaitGroup
	if progName == "redo" {
		for _, arg := range os.Args[1:] {
			n, err = NewNode(arg)
			if err != nil {
				log.Fatalln(fmt.Errorf("failed to stat %s: %v", arg, err))
			}
			if !n.IsTarget {
				log.Fatalln(fmt.Errorf("%s is a source not a target", arg))
			}
			wg.Add(1)
			go func(n *Node) {
				defer wg.Done()
				changed, err := n.RedoIfChange()
				if err != nil {
					log.Fatalln(fmt.Errorf("while building %s: %v", n.Dir+n.File, err))
				}
				if !changed {
					err = n.Build()
					if err != nil {
						log.Fatalln(fmt.Errorf("while building %s: %v", n.Dir+n.File, err))
					}
				}
			}(n)
		}
		wg.Wait()
	}
	if progName == "redo-ifchange" {
		parent := os.Getenv(RedoParentEnv)
		if parent == "" {
			log.Fatalln(fmt.Errorf("redo-ifchange should be called from a do script"))
		}
		prereqsFile, err := os.OpenFile(parent+".prereqs", os.O_APPEND|os.O_WRONLY, 0666)
		if err != nil {
			log.Fatalln(fmt.Errorf("unable to append to prereqs file for %s: %v", RedoParentEnv, err))
		}
		defer prereqsFile.Close()
		for _, arg := range os.Args[1:] {
			n, err = NewNode(arg)
			if err != nil {
				log.Fatalln(fmt.Errorf("failed to stat %s: %v", arg, err))
			}
			wg.Add(1)
			go func(n *Node) {
				defer wg.Done()
				_, err = n.RedoIfChange()
				if err != nil {
					log.Fatalln(fmt.Errorf("while building %s: %v", n.Dir+n.File, err))
				}
			}(n)
		}
		wg.Wait()
		for _, arg := range os.Args[1:] {
			n, err = NewNode(arg)
			if err != nil {
				log.Fatalln(fmt.Errorf("failed to stat %s: %v", arg, err))
			}
			err = n.AddDep(prereqsFile)
			if err != nil {
				log.Fatalln(fmt.Errorf("unable to add dependency: %v", err))
			}
		}
	}
	if progName == "redo-ifcreate" {
		parent := os.Getenv(RedoParentEnv)
		if parent == "" {
			log.Fatalln(fmt.Errorf("redo-ifcreate should be called from a do script"))
		}
		for _, arg := range os.Args[1:] {
			n, err = NewNode(arg)
			if err != nil {
				log.Fatalln(fmt.Errorf("failed to stat %s: %v", arg, err))
			}
			_, err = n.RedoIfCreate()
			if err != nil {
				log.Fatalln(fmt.Errorf("while building %s: %v", n.Dir+n.File, err))
			}
		}
		prereqsFile, err := os.OpenFile(parent+".prereqs", os.O_APPEND|os.O_WRONLY, 0666)
		if err != nil {
			log.Fatalln(fmt.Errorf("unable to append to prereqs file for %s: %v", RedoParentEnv, err))
		}
		defer prereqsFile.Close()
		for _, arg := range os.Args[1:] {
			_, err = fmt.Fprintf(prereqsFile, "%s	ifcreate\n", arg)
			if err != nil {
				log.Fatalln(fmt.Errorf("unable to add ifcreate dep: %v", err))
			}
		}
	}
	if progName == "redo-unless-change" {
		parent := os.Getenv(RedoParentEnv)
		if parent == "" {
			log.Fatalln(fmt.Errorf("redo-unless-change should be called from a do script"))
		}
		for _, arg := range os.Args[1:] {
			n, err = NewNode(arg)
			if err != nil {
				log.Fatalln(fmt.Errorf("failed to stat %s: %v", arg, err))
			}
			err = n.RedoUnlessChange()
			if err != nil {
				log.Fatalln(fmt.Errorf("while building %s: %v", n.Dir+n.File, err))
			}
		}
	}
	return
}
