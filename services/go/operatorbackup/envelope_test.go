package operatorbackup

import (
	"bytes"
	"testing"
)

func TestEnvelopeAndSignedReviewAuthentication(t *testing.T) {
	public, private, err := Generate()
	if err != nil {
		t.Fatal(err)
	}
	pub, err := Public(public)
	if err != nil {
		t.Fatal(err)
	}
	priv, err := Private(private)
	if err != nil {
		t.Fatal(err)
	}
	plain := []byte("snapshot contains a private database credential")
	sealed, err := Seal(plain, pub)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(sealed, plain) {
		t.Fatal("plaintext leaked")
	}
	reopened, err := Open(sealed, priv)
	if err != nil || !bytes.Equal(reopened, plain) {
		t.Fatal("roundtrip failed", err)
	}
	for _, offset := range []int{0, len(magic) + 2, len(sealed) - 1} {
		damaged := append([]byte{}, sealed...)
		damaged[offset] ^= 1
		if _, err = Open(damaged, priv); err == nil {
			t.Fatal("tampered snapshot authenticated")
		}
	}
	if _, err = Open(sealed[:10], priv); err == nil {
		t.Fatal("truncated envelope accepted")
	}
	review := Receipt{Version: 1, Host: "production.example.com", SnapshotID: string(bytes.Repeat([]byte("a"), 64)), EnvelopeSHA256: string(bytes.Repeat([]byte("b"), 64))}
	if err = review.Sign(priv); err != nil {
		t.Fatal(err)
	}
	if err = review.Verify(pub, "production.example.com"); err != nil {
		t.Fatal(err)
	}
	if err = review.Verify(pub, "preview.example.com"); err == nil {
		t.Fatal("cross-host review accepted")
	}
	review.SnapshotID = string(bytes.Repeat([]byte("c"), 64))
	if err = review.Verify(pub, "production.example.com"); err == nil {
		t.Fatal("modified review accepted")
	}
}
