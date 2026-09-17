//go:build backupintegration

package backup_test

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"debug/elf"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"maragu.dev/goqite"
	_ "modernc.org/sqlite"
)

// goqiteSchema mirrors the pinned module version's schema_sqlite.sql. goqite
// does not export a migration helper, so the schema is copied here; it must
// move together with the pinned module version.
const goqiteSchema = `
create table goqite (
  id text primary key default ('m_' || lower(hex(randomblob(16)))),
  created text not null default (strftime('%Y-%m-%dT%H:%M:%fZ')),
  updated text not null default (strftime('%Y-%m-%dT%H:%M:%fZ')),
  queue text not null,
  body blob not null,
  timeout text not null default (strftime('%Y-%m-%dT%H:%M:%fZ')),
  received integer not null default 0,
  priority integer not null default 0
) strict;

create trigger goqite_updated_timestamp after update on goqite begin
  update goqite set updated = strftime('%Y-%m-%dT%H:%M:%fZ') where id = old.id;
end;

create index goqite_queue_priority_created_idx on goqite (queue, priority desc, created);
`

const sqliteDSNSuffix = "?_pragma=journal_mode(WAL)&_pragma=synchronous(FULL)&_pragma=busy_timeout(5000)"

func openGuestDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path+sqliteDSNSuffix)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	return db
}

func TestGuestBinaryAndProcessHelper(t *testing.T) {
	f, err := elf.Open(helper(t))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for _, p := range f.Progs {
		if p.Type == elf.PT_INTERP {
			t.Fatal("guest helper needs a dynamic loader")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, helper(t), "smoke", filepath.Join(t.TempDir(), "state.db")).CombinedOutput()
	if err != nil || strings.TrimSpace(string(out)) != "ok" {
		t.Fatalf("guest smoke: %v: %s", err, out)
	}
	cmd := exec.CommandContext(ctx, helper(t), "process")
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	outPipe, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	scanner := bufio.NewScanner(outPipe)
	if !scanner.Scan() || !strings.HasPrefix(scanner.Text(), "ready ") {
		t.Fatal("helper did not start")
	}
	if _, err := fmt.Fprintln(in, "spawn"); err != nil {
		t.Fatal(err)
	}
	if !scanner.Scan() || !strings.HasPrefix(scanner.Text(), "child ") {
		t.Fatal("helper did not spawn a child")
	}
	if _, err := fmt.Fprintln(in, "exit 7"); err != nil {
		t.Fatal(err)
	}
	var exit *exec.ExitError
	if err := cmd.Wait(); !errors.As(err, &exit) || exit.ExitCode() != 7 {
		t.Fatalf("expected exit 7, got %v", err)
	}
}

func TestSQLiteWALReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	open := func() *sql.DB {
		db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=synchronous(FULL)&_pragma=busy_timeout(5000)")
		if err != nil {
			t.Fatal(err)
		}
		db.SetMaxOpenConns(1)
		return db
	}
	db := open()
	defer db.Close()
	var mode string
	var sync int
	if err := db.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil || mode != "wal" {
		t.Fatalf("WAL: %s, %v", mode, err)
	}
	if err := db.QueryRow("PRAGMA synchronous").Scan(&sync); err != nil || sync != 2 {
		t.Fatalf("FULL: %d, %v", sync, err)
	}
	if _, err := db.Exec("CREATE TABLE receipt (id TEXT PRIMARY KEY, payload BLOB NOT NULL)"); err != nil {
		t.Fatal(err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec("INSERT INTO receipt VALUES (?, ?)", "accepted", []byte("intent")); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	tx, err = db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec("INSERT INTO receipt VALUES (?, ?)", "rolled-back", []byte("intent")); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db = open()
	defer db.Close()
	var payload []byte
	if err := db.QueryRow("SELECT payload FROM receipt WHERE id='accepted'").Scan(&payload); err != nil || !bytes.Equal(payload, []byte("intent")) {
		t.Fatalf("receipt: %q %v", payload, err)
	}
	var count int
	if err := db.QueryRow("SELECT count(*) FROM receipt").Scan(&count); err != nil || count != 1 {
		t.Fatalf("transaction boundaries: %d %v", count, err)
	}
}

// TestGoqiteClaimIsExclusiveAndDurable proves the guarantees our design
// relies on goqite for: concurrent Receive never double-claims a message,
// claim/heartbeat state survives a process-level reopen (not just an
// in-memory cache), a lapsed heartbeat makes a task reclaimable with no
// recoverer daemon involved, and an exhausted MaxReceive count leaves the
// row in place for our own reconciliation rather than silently discarding it.
func TestGoqiteClaimIsExclusiveAndDurable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	db := openGuestDB(t, path)
	defer db.Close()
	if _, err := db.Exec(goqiteSchema); err != nil {
		t.Fatal(err)
	}
	q := goqite.New(goqite.NewOpts{DB: db, Name: "mutation", Timeout: 2 * time.Second, MaxReceive: 2})

	for i := 0; i < 5; i++ {
		if err := q.Send(context.Background(), goqite.Message{Body: fmt.Appendf(nil, "m-%d", i)}); err != nil {
			t.Fatal(err)
		}
	}
	seen := make(chan goqite.ID, 5)
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				m, err := q.Receive(context.Background())
				if err != nil {
					t.Error(err)
					return
				}
				if m != nil {
					seen <- m.ID
					return
				}
				time.Sleep(10 * time.Millisecond)
			}
		}()
	}
	wg.Wait()
	close(seen)
	ids := map[goqite.ID]bool{}
	for id := range seen {
		if ids[id] {
			t.Fatalf("message %s claimed twice under concurrent Receive", id)
		}
		ids[id] = true
	}
	if len(ids) != 5 {
		t.Fatalf("expected 5 distinct concurrent claims, got %d", len(ids))
	}

	id, err := q.SendAndGetID(context.Background(), goqite.Message{Body: []byte("durable")})
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := q.Receive(context.Background())
	if err != nil || claimed == nil || claimed.ID != id {
		t.Fatalf("claim: %+v %v", claimed, err)
	}
	if err := q.Extend(context.Background(), id, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db = openGuestDB(t, path)
	defer db.Close()
	q = goqite.New(goqite.NewOpts{DB: db, Name: "mutation", Timeout: 2 * time.Second, MaxReceive: 2})
	if m, err := q.Receive(context.Background()); err != nil || m != nil {
		t.Fatalf("extended claim did not survive process reopen: %+v %v", m, err)
	}
	var receivedCount int
	if err := db.QueryRow("select received from goqite where id = ?", string(id)).Scan(&receivedCount); err != nil || receivedCount != 1 {
		t.Fatalf("claim count after reopen: %d %v", receivedCount, err)
	}

	// This is the second claim (the first was in step 2 above), and
	// MaxReceive is 2: it exhausts the task's receive budget.
	eventually(t, 10*time.Second, "reclaim after heartbeat lapse", func() bool {
		m, err := q.Receive(context.Background())
		return err == nil && m != nil && m.ID == id
	})

	// goqite leaves an exhausted row in place rather than deleting it: our
	// own reconciliation, not goqite, must find and report it.
	time.Sleep(2500 * time.Millisecond)
	if m, err := q.Receive(context.Background()); err != nil || m != nil {
		t.Fatalf("task past MaxReceive must not be receivable: %+v %v", m, err)
	}
	var stillPresent int
	if err := db.QueryRow("select count(*) from goqite where id = ?", string(id)).Scan(&stillPresent); err != nil || stillPresent != 1 {
		t.Fatalf("exhausted task row must remain for reconciliation, got count=%d: %v", stillPresent, err)
	}
}

// TestGoqiteCrashRecoveryAcrossProcesses proves recovery from a genuinely
// killed handler process using nothing but the file the two processes share:
// a worker claims a task and is SIGKILLed mid-work, and a second worker,
// started immediately after, only has to wait out the crashed claim's own
// heartbeat window before it can claim and finish the same task. There is no
// separate recoverer daemon or lease-scanning interval to wait on.
func TestGoqiteCrashRecoveryAcrossProcesses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	db := openGuestDB(t, path)
	defer db.Close()
	if _, err := db.Exec(goqiteSchema); err != nil {
		t.Fatal(err)
	}
	q := goqite.New(goqite.NewOpts{DB: db, Name: "mutation", Timeout: 3 * time.Second})
	id, err := q.SendAndGetID(context.Background(), goqite.Message{Body: []byte("recover")})
	if err != nil {
		t.Fatal(err)
	}

	startWorker := func(mode string) (*exec.Cmd, <-chan string) {
		cmd := exec.Command(helper(t), "worker", path, mode)
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
		lines := make(chan string, 8)
		go func() {
			defer close(lines)
			s := bufio.NewScanner(stdout)
			for s.Scan() {
				lines <- s.Text()
			}
		}()
		return cmd, lines
	}

	worker, lines := startWorker("block")
	select {
	case line := <-lines:
		if line != "started" {
			t.Fatalf("worker: %s", line)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("worker did not start")
	}
	if err := worker.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := worker.Wait(); err == nil {
		t.Fatal("worker was not killed")
	}

	_, lines = startWorker("complete")
	var sawStarted, sawCompleted bool
	deadline := time.After(15 * time.Second)
	for !sawCompleted {
		select {
		case line, ok := <-lines:
			if !ok {
				t.Fatal("completing worker exited before finishing")
			}
			switch line {
			case "started":
				sawStarted = true
			case "completed":
				sawCompleted = true
			}
		case <-deadline:
			t.Fatal("completing worker never finished within the crashed claim's heartbeat window")
		}
	}
	if !sawStarted {
		t.Fatal("completing worker finished without ever reporting a claim")
	}
	var remaining int
	if err := db.QueryRow("select count(*) from goqite where id = ?", string(id)).Scan(&remaining); err != nil || remaining != 0 {
		t.Fatalf("expected task row deleted after completion, count=%d: %v", remaining, err)
	}
}
