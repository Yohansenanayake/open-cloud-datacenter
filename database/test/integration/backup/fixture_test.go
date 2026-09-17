//go:build backupintegration

package backup_test

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

const rabbitImage = "rabbitmq:4.3.5@sha256:1773ab33af6af1bbae4a3b1070472c863e669806a9ac63a627c02a44b3a59dcf"

func docker(t *testing.T, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("docker %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func container(t *testing.T, args ...string) string {
	t.Helper()
	id := docker(t, append([]string{"run", "-d", "--label", "dbaas.backup.fixture=true"}, args...)...)
	t.Cleanup(func() {
		if t.Failed() {
			t.Log(docker(t, "logs", "--tail", "80", id))
		}
		docker(t, "rm", "-fv", id)
	})
	return id
}

func eventually(t *testing.T, timeout time.Duration, description string, f func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if f() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timed out: %s", description)
}

func helper(t *testing.T) string {
	t.Helper()
	path := os.Getenv("BACKUP_RUNTIME_HELPER")
	if path == "" {
		t.Fatal("run make test-backup-integration to build the pinned guest helper")
	}
	return path
}
