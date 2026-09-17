//go:build backupintegration

// Runtime qualification helper; this is not a guest service.
package main

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"time"

	"filippo.io/age"
	"github.com/rabbitmq/rabbitmq-amqp-go-client/pkg/rabbitmqamqp"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/wrapperspb"
	"maragu.dev/goqite"
	_ "modernc.org/sqlite"
)

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

func main() {
	if len(os.Args) < 2 {
		panic("expected smoke, process, or worker")
	}
	switch os.Args[1] {
	case "smoke":
		if err := smoke(os.Args[2]); err != nil {
			panic(err)
		}
		fmt.Println("ok")
	case "process":
		os.Exit(process())
	case "worker":
		worker(os.Args[2], os.Args[3])
	default:
		panic("unknown mode")
	}
}

// stdin controls blocking, child creation, and exit without a shell or sleeps.
func process() int {
	fmt.Printf("ready %d\n", os.Getpid())
	var children []*exec.Cmd
	defer func() {
		for _, child := range children {
			_ = child.Process.Kill()
			_ = child.Wait()
		}
	}()
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "spawn" {
			child := exec.Command(os.Args[0], "process")
			// A failed or timed-out test must not leave the fixture child behind.
			child.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
			// Keep the child blocked on its own input until the parent exits.
			in, err := child.StdinPipe()
			if err != nil {
				panic(err)
			}
			defer in.Close()
			if err := child.Start(); err != nil {
				panic(err)
			}
			children = append(children, child)
			fmt.Printf("child %d\n", child.Process.Pid)
		} else if len(line) > 5 && line[:5] == "exit " {
			code, err := strconv.Atoi(line[5:])
			if err != nil || code < 0 || code > 255 {
				panic("invalid exit code")
			}
			return code
		}
	}
	if scanner.Err() != nil {
		return 1
	}
	return 0
}

// worker claims exactly one task on the shared SQLite-backed queue and either
// blocks (heartbeating like a legitimately long-running handler, until an
// external SIGKILL stops it mid-work) or completes and deletes it. There is
// no separate recoverer to wait on: a killed "block" worker's task becomes
// reclaimable purely because nothing extends its heartbeat any more.
func worker(path, mode string) {
	db, err := sql.Open("sqlite", path+sqliteDSNSuffix)
	if err != nil {
		panic(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	q := goqite.New(goqite.NewOpts{DB: db, Name: "mutation", Timeout: 3 * time.Second})
	ctx := context.Background()
	m, err := q.ReceiveAndWait(ctx, 100*time.Millisecond)
	if err != nil {
		panic(err)
	}
	fmt.Println("started")
	if mode == "block" {
		for {
			time.Sleep(time.Second)
			if err := q.Extend(ctx, m.ID, 3*time.Second); err != nil {
				return
			}
		}
	}
	if err := q.Delete(ctx, m.ID); err != nil {
		panic(err)
	}
	fmt.Println("completed")
}

// Exercise all guest dependencies in the actual CGO-disabled executable.
func smoke(path string) error {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return err
	}
	defer db.Close()
	if _, err = db.Exec("CREATE TABLE smoke (value TEXT); INSERT INTO smoke VALUES ('durable')"); err != nil {
		return err
	}
	var value string
	if err = db.QueryRow("SELECT value FROM smoke").Scan(&value); err != nil {
		return err
	}
	if value != "durable" {
		return errors.New("SQLite roundtrip failed")
	}
	if _, err = db.Exec(goqiteSchema); err != nil {
		return err
	}
	q := goqite.New(goqite.NewOpts{DB: db, Name: "smoke", Timeout: time.Second})
	id, err := q.SendAndGetID(context.Background(), goqite.Message{Body: []byte("fixture")})
	if err != nil {
		return err
	}
	claimed, err := q.Receive(context.Background())
	if err != nil {
		return err
	}
	if claimed == nil || claimed.ID != id || string(claimed.Body) != "fixture" {
		return errors.New("goqite claim roundtrip failed")
	}
	if err := q.Delete(context.Background(), id); err != nil {
		return err
	}
	key, err := age.GenerateX25519Identity()
	if err != nil {
		return err
	}
	var ciphertext bytes.Buffer
	w, err := age.Encrypt(&ciphertext, key.Recipient())
	if err != nil {
		return err
	}
	if _, err = w.Write([]byte("fixture credential")); err != nil {
		return err
	}
	if err = w.Close(); err != nil {
		return err
	}
	r, err := age.Decrypt(bytes.NewReader(ciphertext.Bytes()), key)
	if err != nil {
		return err
	}
	plain, err := io.ReadAll(r)
	if err != nil || string(plain) != "fixture credential" {
		return errors.New("age roundtrip failed")
	}
	wrong, err := age.GenerateX25519Identity()
	if err != nil {
		return err
	}
	if _, err = age.Decrypt(bytes.NewReader(ciphertext.Bytes()), wrong); err == nil {
		return errors.New("wrong key accepted")
	}
	corrupt := bytes.Clone(ciphertext.Bytes())
	corrupt[len(corrupt)-1] ^= 1
	r, err = age.Decrypt(bytes.NewReader(corrupt), key)
	if err == nil {
		_, err = io.ReadAll(r)
	}
	if err == nil {
		return errors.New("tampered ciphertext accepted")
	}
	payload, err := proto.Marshal(wrapperspb.String("fixture"))
	if err != nil {
		return err
	}
	_, err = rabbitmqamqp.NewMessageWithAddress(payload, &rabbitmqamqp.QueueAddress{Queue: "fixture"})
	return err
}
