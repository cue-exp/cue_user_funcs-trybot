package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"testing"

	"github.com/rogpeppe/go-internal/testscript"
)

var update = flag.Bool("update", false, "update testscript golden files")

// runCue provides a "cue" command for testscript CLI tests.
// It reads the GOTEST_CUE_PATH env var (set by Setup) to find
// the cue binary resolved from "go tool -n cue".
func runCue() int {
	cuePath := os.Getenv("GOTEST_CUE_PATH")
	if cuePath == "" {
		fmt.Fprintln(os.Stderr, "GOTEST_CUE_PATH not set")
		return 1
	}
	cmd := exec.Command(cuePath, os.Args[1:]...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return exitErr.ExitCode()
		}
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

func TestMain(m *testing.M) {
	testscript.Main(m, map[string]func(){
		"cue_user_funcs": func() {
			os.Exit(main1())
		},
		"cue": func() {
			os.Exit(runCue())
		},
	})
}

func TestScript(t *testing.T) {
	// Resolve the cue binary path once via "go tool -n cue".
	cuePath, err := exec.Command("go", "tool", "-n", "cue").Output()
	if err != nil {
		t.Fatalf("resolving cue tool path: %v", err)
	}

	// Get GOPATH and GOCACHE from go env so we can share caches.
	goEnvOut, err := exec.Command("go", "env", "-json", "GOPATH", "GOCACHE").Output()
	if err != nil {
		t.Fatalf("resolving go env: %v", err)
	}
	var goEnv struct {
		GOPATH  string
		GOCACHE string
	}
	if err := json.Unmarshal(goEnvOut, &goEnv); err != nil {
		t.Fatalf("parsing go env: %v", err)
	}

	testscript.Run(t, testscript.Params{
		Dir:           "testdata",
		UpdateScripts: *update,
		Setup: func(e *testscript.Env) error {
			e.Vars = append(e.Vars,
				"GOTEST_CUE_PATH="+string(cuePath[:len(cuePath)-1]),
				"GOPATH="+goEnv.GOPATH,
				"GOCACHE="+goEnv.GOCACHE,
			)
			return nil
		},
	})
}
