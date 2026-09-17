//go:build backupintegration

package backup_test

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rabbitmq/rabbitmq-amqp-go-client/pkg/rabbitmqamqp"
)

func TestAMQP10TLSSettlement(t *testing.T) {
	dir := t.TempDir()
	// Only disposable test keys are mounted read-only for the container user.
	if err := os.Chmod(dir, 0755); err != nil {
		t.Fatal(err)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cert := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "backup fixture"},
		NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	files := map[string][]byte{
		"cert.pem":      certPEM,
		"key.pem":       pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}),
		"rabbitmq.conf": []byte("listeners.tcp = none\nlisteners.ssl.default = 5671\nssl_options.cacertfile = /fixture/cert.pem\nssl_options.certfile = /fixture/cert.pem\nssl_options.keyfile = /fixture/key.pem\nssl_options.verify = verify_none\nssl_options.fail_if_no_peer_cert = false\ndefault_user = fixture\ndefault_pass = disposable-fixture-only\n"),
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), content, 0644); err != nil {
			t.Fatal(err)
		}
	}
	id := container(t, "--publish", "127.0.0.1::5671", "--mount", "type=bind,src="+dir+",dst=/fixture,readonly",
		"--env", "RABBITMQ_CONFIG_FILE=/fixture/rabbitmq", "--env", "RABBITMQ_SERVER_ADDITIONAL_ERL_ARGS=+S 2:2", rabbitImage)
	address := strings.TrimSpace(docker(t, "port", id, "5671/tcp"))
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(certPEM) {
		t.Fatal("invalid fixture certificate")
	}
	options := &rabbitmqamqp.AmqpConnOptions{TLSConfig: &tls.Config{RootCAs: pool, ServerName: "127.0.0.1", MinVersion: tls.VersionTLS12}}
	uri := "amqps://fixture:disposable-fixture-only@" + address
	// Wait for the TLS listener before invoking the client's logging dial path.
	eventually(t, 90*time.Second, "RabbitMQ TLS listener", func() bool {
		c, err := tls.DialWithDialer(&net.Dialer{Timeout: time.Second}, "tcp", address, options.TLSConfig)
		if err != nil {
			return false
		}
		_ = c.Close()
		return true
	})
	var conn *rabbitmqamqp.AmqpConnection
	eventually(t, 90*time.Second, "RabbitMQ TLS and AMQP 1.0 readiness", func() bool {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		conn, err = rabbitmqamqp.Dial(ctx, uri, options)
		return err == nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	defer conn.Close(context.Background())
	// The same endpoint must fail with an untrusted certificate pool.
	untrusted, err := rabbitmqamqp.Dial(ctx, uri, &rabbitmqamqp.AmqpConnOptions{TLSConfig: &tls.Config{RootCAs: x509.NewCertPool(), ServerName: "127.0.0.1", MinVersion: tls.VersionTLS12}})
	if err == nil {
		_ = untrusted.Close(ctx)
		t.Fatal("untrusted TLS certificate accepted")
	}
	var unknownCA x509.UnknownAuthorityError
	if !errors.As(err, &unknownCA) {
		t.Fatalf("expected certificate trust failure, got %v", err)
	}
	_, err = conn.Management().DeclareQueue(ctx, &rabbitmqamqp.QuorumQueueSpecification{Name: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	publisher, err := conn.NewPublisher(ctx, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer publisher.Close(context.Background())
	message, err := rabbitmqamqp.NewMessageWithAddress([]byte("opaque protobuf fixture"), &rabbitmqamqp.QueueAddress{Queue: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := publisher.Publish(ctx, message)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := result.Outcome.(*rabbitmqamqp.StateAccepted); !ok {
		t.Fatalf("publisher outcome: %T", result.Outcome)
	}
	consumer, err := conn.NewConsumer(ctx, "fixture", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer consumer.Close(context.Background())
	delivery, err := consumer.Receive(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(delivery.Message().GetData(), message.GetData()) {
		t.Fatal("AMQP data section changed")
	}
	if err := delivery.Requeue(ctx); err != nil {
		t.Fatal(err)
	}
	delivery, err = consumer.Receive(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(delivery.Message().GetData(), message.GetData()) {
		t.Fatal("redelivered data changed")
	}
	if err := delivery.Accept(ctx); err != nil {
		t.Fatal(err)
	}
	check, done := context.WithTimeout(ctx, time.Second)
	defer done()
	if _, err := consumer.Receive(check); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected an empty live consumer after settlement, got %v", err)
	}
}
