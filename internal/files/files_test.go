package files

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"strings"
	"testing"
	"time"
)

func TestStorageKey(t *testing.T) {
	digest := sha256.Sum256([]byte("hello"))
	full := hex.EncodeToString(digest[:])
	key := StorageKey(full, "hello.txt")
	if key == "" {
		t.Fatal("empty key")
	}
	if !strings.HasPrefix(key, full[:2]+"/") {
		t.Errorf("missing 2-char prefix: %q", key)
	}
	if !strings.HasSuffix(key, "/hello.txt") {
		t.Errorf("missing filename: %q", key)
	}
	// Truncated digest must reject.
	if StorageKey("abc", "x") != "" {
		t.Error("truncated digest accepted")
	}
}

func TestSanitiseFilename(t *testing.T) {
	cases := []struct{ in, want string }{
		{"hello.txt", "hello.txt"},
		{"My Photo (1).jpg", "My_Photo_1_.jpg"},
		// `..` chars survive as ordinary text once flattened; the
		// danger is the path separator, which is replaced.
		{"../../etc/passwd", "_.._etc_passwd"},
		{"", "file"},
		{".hidden", "hidden"},
		{"a/b/c.png", "a_b_c.png"},
		// Literal `_` chars in the input are kept verbatim — only
		// consecutive *replacement* underscores collapse.
		{"x  y", "x_y"},
	}
	for _, c := range cases {
		if got := SanitiseFilename(c.in); got != c.want {
			t.Errorf("Sanitise(%q)=%q want %q", c.in, got, c.want)
		}
	}
}

func TestSignURL_RoundTrip(t *testing.T) {
	key := []byte("master-secret")
	token, expires := SignURL(key, "posts", "rec-1", "cover", "image.png", 30*time.Second)
	if token == "" || expires == "" {
		t.Fatal("empty token/expires")
	}
	if err := VerifySignature(key, "posts", "rec-1", "cover", "image.png", token, expires); err != nil {
		t.Errorf("verify: %v", err)
	}
}

func TestSignURL_RejectsTamper(t *testing.T) {
	key := []byte("master-secret")
	tok, exp := SignURL(key, "posts", "r1", "cover", "image.png", 30*time.Second)

	// Tamper filename.
	if err := VerifySignature(key, "posts", "r1", "cover", "OTHER.png", tok, exp); err == nil {
		t.Error("filename tamper accepted")
	}
	// Tamper field.
	if err := VerifySignature(key, "posts", "r1", "avatar", "image.png", tok, exp); err == nil {
		t.Error("field tamper accepted")
	}
	// Tamper token byte. The original first char may have been '0' —
	// hex tokens start with 0-9a-f, so a fixed substitution like "0"+
	// has a ~1/16 chance of leaving the bytes unchanged and producing
	// a flake. Pick a char guaranteed to differ from the original.
	flip := byte('a')
	if tok[0] == flip {
		flip = 'b'
	}
	bad := string(flip) + tok[1:]
	if err := VerifySignature(key, "posts", "r1", "cover", "image.png", bad, exp); err == nil {
		t.Error("byte tamper accepted")
	}
	// Wrong key.
	if err := VerifySignature([]byte("other-key"), "posts", "r1", "cover", "image.png", tok, exp); err == nil {
		t.Error("wrong key accepted")
	}
}

func TestSignURL_RejectsExpired(t *testing.T) {
	key := []byte("master-secret")
	// Negative TTL — already past.
	tok, exp := SignURL(key, "posts", "r1", "cover", "image.png", -1*time.Second)
	if err := VerifySignature(key, "posts", "r1", "cover", "image.png", tok, exp); err == nil {
		t.Error("expired token accepted")
	}
}

func TestFSDriver_PutOpenDelete(t *testing.T) {
	dir := t.TempDir()
	d, err := NewFSDriver(dir)
	if err != nil {
		t.Fatal(err)
	}

	body := []byte("hello world this is some content")
	digest := sha256.Sum256(body)
	full := hex.EncodeToString(digest[:])
	key := StorageKey(full, "hello.txt")

	n, err := d.Put(context.Background(), key, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	if n != int64(len(body)) {
		t.Errorf("bytes written = %d, want %d", n, len(body))
	}

	r, err := d.Open(context.Background(), key)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer r.Close()
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, body) {
		t.Errorf("round-trip mismatch: %q vs %q", got, body)
	}

	// Seek works (Range support).
	if _, err := r.Seek(6, io.SeekStart); err != nil {
		t.Errorf("seek: %v", err)
	}
	rest, _ := io.ReadAll(r)
	if string(rest) != "world this is some content" {
		t.Errorf("seek payload: %q", rest)
	}

	// Delete.
	if err := d.Delete(context.Background(), key); err != nil {
		t.Errorf("delete: %v", err)
	}
	if _, err := d.Open(context.Background(), key); err != ErrNotFound {
		t.Errorf("expected ErrNotFound after delete, got %v", err)
	}

	// Idempotent delete.
	if err := d.Delete(context.Background(), key); err != nil {
		t.Errorf("idempotent delete: %v", err)
	}
}

func TestFSDriver_RejectsTraversal(t *testing.T) {
	d, err := NewFSDriver(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// ".." in key.
	if _, err := d.Put(context.Background(), "../escape.txt", bytes.NewReader(nil)); err == nil {
		t.Error("traversal '../' accepted")
	}
	// Absolute path.
	if _, err := d.Put(context.Background(), "/etc/passwd", bytes.NewReader(nil)); err == nil {
		t.Error("absolute path accepted")
	}
}

func TestHashingReader(t *testing.T) {
	body := []byte("the quick brown fox")
	r := NewHashingReader(bytes.NewReader(body))
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out, body) {
		t.Errorf("payload differs")
	}
	want := sha256.Sum256(body)
	if !bytes.Equal(r.Sum(), want[:]) {
		t.Errorf("hash differs")
	}
	if r.Size() != int64(len(body)) {
		t.Errorf("size = %d want %d", r.Size(), len(body))
	}
}
