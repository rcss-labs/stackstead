//go:build integration

package s3

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/rcss-labs/stackstead/internal/registry/awsauth"
	"github.com/rcss-labs/stackstead/internal/secret"
)

// Pinned release; never use latest.
const minioImage = "minio/minio:RELEASE.2025-01-20T14-49-07Z"

func TestMinioRoundTrip(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not installed")
	}
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skip("docker daemon unavailable")
	}
	const access, sec = "minioadmin", "minioadmin-secret"
	out, err := exec.Command("docker", "run", "-d", "--rm", "-p", "127.0.0.1::9000",
		"-e", "MINIO_ROOT_USER="+access, "-e", "MINIO_ROOT_PASSWORD="+sec,
		minioImage, "server", "/data").Output()
	if err != nil {
		t.Skipf("cannot start MinIO container: %v", err)
	}
	id := strings.TrimSpace(string(out))
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", id).Run() })

	portOut, err := exec.Command("docker", "port", id, "9000/tcp").Output()
	if err != nil {
		t.Fatal(err)
	}
	endpoint := "http://" + strings.TrimSpace(strings.SplitN(string(portOut), "\n", 2)[0])
	waitReady(t, endpoint)

	c := New(Config{Bucket: "backups", Region: "us-east-1", Endpoint: endpoint, PathStyle: true,
		Creds: awsauth.Credentials{AccessKeyID: access, SecretAccessKey: secret.Secret(sec)}}, nil, nil)
	ctx := context.Background()
	createBucket(t, c, ctx)

	data := make([]byte, 40<<20)
	_, _ = rand.Read(data)
	u, err := c.NewUpload(ctx, "stackstead/big.bin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(u, bytes.NewReader(data)); err != nil {
		t.Fatal(err)
	}
	if err := u.Close(); err != nil {
		t.Fatal(err)
	}

	objs, err := c.List(ctx, "stackstead/")
	if err != nil || len(objs) != 1 || objs[0].Key != "stackstead/big.bin" || objs[0].Size != int64(len(data)) {
		t.Fatalf("List = %+v, %v", objs, err)
	}
	rc, size, err := c.Get(ctx, "stackstead/big.bin")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(rc)
	rc.Close()
	if size != int64(len(data)) || sha256.Sum256(got) != sha256.Sum256(data) {
		t.Fatalf("downloaded object differs (size %d)", size)
	}
	if err := c.Delete(ctx, "stackstead/big.bin"); err != nil {
		t.Fatal(err)
	}
	if objs, _ := c.List(ctx, "stackstead/"); len(objs) != 0 {
		t.Fatalf("object still listed after delete: %+v", objs)
	}
}

func waitReady(t *testing.T, endpoint string) {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		if resp, err := http.Get(endpoint + "/minio/health/ready"); err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				return
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatal("MinIO did not become ready")
}

// createBucket issues a signed CreateBucket (PUT /<bucket>) with the client's own signer.
func createBucket(t *testing.T, c *Client, ctx context.Context) {
	t.Helper()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPut, c.cfg.Endpoint+"/"+c.cfg.Bucket, nil)
	if err := awsauth.SignV4(req, nil, c.cfg.Creds, c.cfg.Region, "s3", time.Now()); err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		t.Fatalf("create bucket: HTTP %d", resp.StatusCode)
	}
}
