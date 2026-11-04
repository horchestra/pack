package git

import (
	"bytes"
	"context"
	"crypto/sha512"
	"encoding/binary"
	"encoding/pem"
	"fmt"
	"io"
	"strings"

	git "github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	gossh "golang.org/x/crypto/ssh"
)

// VerifyCommit says whether the commit is signed, SSH or GPG, by one of the project's approval keys (К5: a
// Pack-Approve or Pack-Merge trailer acts only signed; keys are "ssh-ed25519 AAAA… comment" lines, or GPG
// public key blocks).
func VerifyCommit(ctx context.Context, dir, sha string, keys []string) (bool, error) {
	var sshKeys, gpgKeys []string
	for _, k := range keys {
		k = strings.TrimSpace(k)
		switch {
		case len(k) == 0:
		case strings.HasPrefix(k, "ssh-"), strings.HasPrefix(k, "ecdsa-"), strings.HasPrefix(k, "sk-"):
			sshKeys = append(sshKeys, k)
		default:
			gpgKeys = append(gpgKeys, k)
		}
	}
	repo, err := git.PlainOpen(dir)
	if err != nil {
		return false, err
	}
	commit, err := repo.CommitObject(plumbing.NewHash(sha))
	if err != nil {
		return false, fmt.Errorf("%s: %w", sha, err)
	}
	if len(commit.Signature) == 0 {
		return false, nil
	}
	obj, err := repo.Storer.EncodedObject(plumbing.CommitObject, commit.Hash)
	if err != nil {
		return false, err
	}
	rc, err := obj.Reader()
	if err != nil {
		return false, err
	}
	defer rc.Close()
	raw, err := io.ReadAll(rc)
	if err != nil {
		return false, err
	}
	payload := payloadWithoutSignature(raw)
	if strings.Contains(commit.Signature, "BEGIN SSH SIGNATURE") {
		return verifySSH(commit.Signature, payload, sshKeys)
	}
	if len(gpgKeys) == 0 {
		return false, nil
	}
	if _, err := commit.Verify(strings.Join(gpgKeys, "\n")); err != nil {
		return false, nil // a signature that does not check out is a refusal, not an error
	}
	return true, nil
}

// payloadWithoutSignature is what the signature signs: the commit's bytes as stored, the gpgsig header — its
// own line and its continuation lines (each starts with a space) — out, everything else byte for byte.
func payloadWithoutSignature(raw []byte) []byte {
	var out bytes.Buffer
	skipping := false
	for _, line := range strings.SplitAfter(string(raw), "\n") {
		if strings.HasPrefix(line, "gpgsig ") {
			skipping = true
			continue
		}
		if skipping && strings.HasPrefix(line, " ") {
			continue
		}
		skipping = false
		out.WriteString(line)
	}
	return out.Bytes()
}

// verifySSH checks an armored SSH signature (SSHSIG, namespace git) against the allowed keys.
func verifySSH(armored string, payload []byte, keys []string) (bool, error) {
	block, _ := pem.Decode([]byte(armored))
	if block == nil || block.Type != "SSH SIGNATURE" {
		return false, nil
	}
	r := bytes.NewReader(block.Bytes)
	magic := make([]byte, 6)
	if _, err := r.Read(magic); err != nil || string(magic) != "SSHSIG" {
		return false, nil
	}
	var version uint32
	if err := binary.Read(r, binary.BigEndian, &version); err != nil || version != 1 {
		return false, nil
	}
	str := func() ([]byte, bool) {
		var n uint32
		if err := binary.Read(r, binary.BigEndian, &n); err != nil || uint32(r.Len()) < n {
			return nil, false
		}
		b := make([]byte, n)
		_, err := r.Read(b)
		return b, err == nil
	}
	if _, ok := str(); !ok { // the signer's public key: the signature decides, not this
		return false, nil
	}
	ns, ok := str()
	if !ok || string(ns) != "git" {
		return false, nil
	}
	if _, ok := str(); !ok { // reserved
		return false, nil
	}
	alg, ok := str()
	if !ok || string(alg) != "sha512" {
		return false, nil
	}
	rawSig, ok := str()
	if !ok {
		return false, nil
	}
	// the signature in SSH wire format: string format, string blob
	sigReader := bytes.NewReader(rawSig)
	readStr := func(r *bytes.Reader) ([]byte, bool) {
		var n uint32
		if err := binary.Read(r, binary.BigEndian, &n); err != nil || uint32(r.Len()) < n {
			return nil, false
		}
		b := make([]byte, n)
		_, err := r.Read(b)
		return b, err == nil
	}
	format, ok := readStr(sigReader)
	if !ok {
		return false, nil
	}
	blob, ok := readStr(sigReader)
	if !ok {
		return false, nil
	}
	sig := &gossh.Signature{Format: string(format), Blob: blob}
	h := sha512.Sum512(payload)
	var signed bytes.Buffer
	signed.WriteString("SSHSIG")
	put := func(b []byte) {
		_ = binary.Write(&signed, binary.BigEndian, uint32(len(b)))
		signed.Write(b)
	}
	put(ns)
	put(nil)
	put([]byte("sha512"))
	put(h[:])
	for _, k := range keys {
		pub, _, _, _, err := gossh.ParseAuthorizedKey([]byte(k))
		if err != nil {
			continue
		}
		if pub.Verify(signed.Bytes(), sig) == nil {
			return true, nil
		}
	}
	return false, nil
}
