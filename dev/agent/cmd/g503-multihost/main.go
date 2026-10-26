package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/yanagihalab/G503/dev/agent/internal/multihost"
)

func main() {
	path := flag.String("inventory", "", "five-host inventory JSON")
	revision := flag.String("revision", "", "override with the full pushed commit SHA")
	results := flag.String("results", "", "new local result directory (required with -execute)")
	execute := flag.Bool("execute", false, "explicitly provision five hosts and inject scoped firewall faults")
	resume := flag.Bool("resume", false, "resume the same private controller journal and immutable inventory")
	cleanup := flag.Bool("cleanup", false, "recover owned resources only; requires -execute -resume")
	duration := flag.Duration("timeout", 90*time.Minute, "overall execution deadline including fresh remote builds")
	flag.Parse()
	inventory, err := multihost.Load(*path)
	if err != nil {
		fail(err)
	}
	if *revision != "" {
		inventory.Revision = *revision
	}
	if (*resume || *cleanup) && !*execute {
		fail(fmt.Errorf("resume/cleanup require -execute"))
	}
	runner := &multihost.Runner{Inventory: inventory, ResultDir: *results, Resume: *resume, CleanupOnly: *cleanup, Transport: multihost.SSHTransport{}, Progress: func(p string) { fmt.Fprintln(os.Stderr, p) }}
	var summary multihost.Summary
	if *execute {
		if *results == "" || *duration <= 0 {
			fail(fmt.Errorf("-results must name a new directory and -timeout must be positive"))
		}
		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer cancel()
		ctx, timeout := context.WithTimeout(ctx, *duration)
		defer timeout()
		summary, err = runner.Execute(ctx)
	} else {
		summary, err = runner.Plan()
	}
	if err != nil {
		fail(err)
	}
	if err = json.NewEncoder(os.Stdout).Encode(summary); err != nil {
		fail(err)
	}
}
func fail(err error) { fmt.Fprintln(os.Stderr, err); os.Exit(1) }
