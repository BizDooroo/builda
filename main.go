package main

import (
	"embed"
	"fmt"
	"os"
	"time"
)

//go:embed all:web/dist
var webDist embed.FS

const (
	StatusQueued   = "QUEUED"
	StatusRunning  = "RUNNING"
	StatusSuccess  = "SUCCESS"
	StatusFailed   = "FAILED"
	StatusCanceled = "CANCELED"
	StatusAborted  = "ABORTED"

	defaultListenAddress = ":28088"
	defaultMaxHistory    = 5000
	taskRunWaitParam     = "wait"
	configReloadInterval = time.Second
	defaultScriptHeader  = "#!/usr/bin/env bash"

	displayTimeLayout = "06-01-02 15:04:05"
)

var (
	version = "dev"
	commit  = ""
	date    = ""
)

func main() {
	if err := newRootCommand().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
