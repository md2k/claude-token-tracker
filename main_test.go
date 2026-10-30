package main

import (
	"os"
	"os/exec"
	"testing"
)

func TestProcessExists(t *testing.T) {
	if !processExists(os.Getpid()) {
		t.Fatal("processExists(own pid) = false, want true")
	}

	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Fatalf("run true: %v", err)
	}
	if processExists(cmd.Process.Pid) {
		t.Errorf("processExists(exited pid %d) = true, want false", cmd.Process.Pid)
	}
}
