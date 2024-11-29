package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"sync"
	"time"
)

const (
	RedoParentEnv   = "REDOPARENT"
	RedoTreeTimeEnv = "REDOTREETIME"
)

var RedoTreeTime time.Time

func main() {
	log.SetFlags(0)
	progName := filepath.Base(os.Args[0])
	log.SetPrefix(progName + ": ")

	t := os.Getenv(RedoTreeTimeEnv)
	if t == "" {
		RedoTreeTime = time.Now()
		os.Setenv(RedoTreeTimeEnv, strconv.FormatInt(RedoTreeTime.Unix(), 10))
	} else {
		t, err := strconv.ParseInt(t, 10, 64)
		if err != nil {
			log.Fatalln("invalid", RedoTreeTimeEnv, t)
		}
		RedoTreeTime = time.Unix(t, 0)
	}

	ctx, cancelCause := context.WithCancelCause(context.Background())
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	go func() {
		<- sig
		cancelCause(errors.New("received signal"))
	}()
	var wg sync.WaitGroup

	switch progName {
	case "redo":
		for _, arg := range os.Args[1:] {
			n, err := NewNode(arg)
			if err != nil {
				cancelCause(fmt.Errorf("failed to stat %s: %v", arg, err))
				break
			}
			if !n.IsTarget {
				cancelCause(fmt.Errorf("%s is a source not a target", arg))
				break
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, err := n.RedoIfChange(ctx, cancelCause)
				if err != nil {
					cancelCause(err)
				}
			}()
		}
		wg.Wait()
	case "redo-ifchange":
		parent := os.Getenv(RedoParentEnv)
		if parent == "" {
			log.Fatalln("redo-ifchange should be called from a do script")
		}
		prereqsFile, err := os.OpenFile(parent+".prereqs", os.O_APPEND|os.O_WRONLY, 0666)
		if err != nil {
			log.Fatalln("unable to append to prereqs file for", RedoParentEnv, err)
		}
		defer prereqsFile.Close()
		for _, arg := range os.Args[1:] {
			n, err := NewNode(arg)
			if err != nil {
				cancelCause(fmt.Errorf("failed to stat %s: %v", arg, err))
				break
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, err = n.RedoIfChange(ctx, cancelCause)
				if err != nil {
					cancelCause(err)
				}
			}()
		}
		wg.Wait()
		if err = context.Cause(ctx); err != nil {
			log.Fatalln(err)
		}
		for _, arg := range os.Args[1:] {
			n, err := NewNode(arg)
			if err != nil {
				log.Fatalln("failed to stat", arg, err)
			}
			err = n.AddDep(prereqsFile)
			if err != nil {
				log.Fatalln("unable to add dependency:", err)
			}
		}
	case "redo-ifcreate":
		parent := os.Getenv(RedoParentEnv)
		if parent == "" {
			log.Fatalln("redo-ifcreate should be called from a do script")
		}
		for _, arg := range os.Args[1:] {
			n, err := NewNode(arg)
			if err != nil {
				log.Fatalln("failed to stat", arg, err)
			}
			_, err = n.RedoIfCreate()
			if err != nil {
				log.Fatalln("while building", n.Dir+n.File, err)
			}
		}
		prereqsFile, err := os.OpenFile(parent+".prereqs", os.O_APPEND|os.O_WRONLY, 0666)
		if err != nil {
			log.Fatalln("unable to append to prereqs file for", RedoParentEnv, err)
		}
		defer prereqsFile.Close()
		for _, arg := range os.Args[1:] {
			_, err = fmt.Fprintf(prereqsFile, "%s	ifcreate\n", arg)
			if err != nil {
				log.Fatalln("unable to add ifcreate dep:", err)
			}
		}
	case "stop-ifchange":
		for _, arg := range os.Args[1:] {
			n, err := NewNode(arg)
			if err != nil {
				log.Fatalln("failed to stat", arg, err)
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				err = n.StopIfChange()
				if err != nil {
					log.Fatalln("while building", n.Dir+n.File, err)
				}
			}()
		}
		wg.Wait()
	default:
		log.Fatalln("Unrecognized executable name:", progName)
	}

	return
}
