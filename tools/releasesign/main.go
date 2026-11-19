/* SPDX-License-Identifier: MIT
 *
 * CFBTEL VPN release signing (signify-compatible Ed25519), used by the release workflow.
 *
 *   releasesign keygen              prints a new public key (for updater/constants.go) and private key (GitHub secret)
 *   releasesign sign OUT FILE...    writes OUT: BLAKE2b-256 hashes of FILEs, signed with $CFBTEL_RELEASE_KEY
 *   releasesign verify SIG PUBKEY   checks a signature file against a public key
 *   releasesign pubkey              prints the public key of $CFBTEL_RELEASE_KEY
 */

package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"golang.org/x/crypto/blake2b"
)

func die(err error) {
	fmt.Fprintln(os.Stderr, "releasesign:", err)
	os.Exit(1)
}

// keygenClipboard (Windows): the private key goes straight to the clipboard, never on screen.
func keygenClipboard() {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		die(err)
	}
	keyNum := make([]byte, 8)
	rand.Read(keyNum)
	cmd := exec.Command("clip.exe")
	cmd.Stdin = strings.NewReader(base64.StdEncoding.EncodeToString(append(append([]byte("Ed"), keyNum...), priv...)))
	if err := cmd.Run(); err != nil {
		die(err)
	}
	fmt.Println("The PRIVATE key is now on your clipboard. Paste it into GitHub:")
	fmt.Println("  github.com/cfbtel/cfbtel-vpn-windows/settings/secrets/actions -> New repository secret")
	fmt.Println("  Name: CFBTEL_RELEASE_KEY    Secret: Ctrl+V    -> Add secret")
	fmt.Println()
	fmt.Println("Then send this PUBLIC key to Claude (it's safe to share):")
	fmt.Println("  " + base64.StdEncoding.EncodeToString(append(append([]byte("Ed"), keyNum...), pub...)))
	fmt.Println()
	fmt.Println("Press Enter to close.")
	fmt.Scanln()
}

func keygen() {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		die(err)
	}
	keyNum := make([]byte, 8)
	rand.Read(keyNum)
	fmt.Println("public:  " + base64.StdEncoding.EncodeToString(append(append([]byte("Ed"), keyNum...), pub...)))
	fmt.Println("private: " + base64.StdEncoding.EncodeToString(append(append([]byte("Ed"), keyNum...), priv...)))
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h, _ := blake2b.New256(nil)
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func sign(out string, files []string) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(os.Getenv("CFBTEL_RELEASE_KEY")))
	if err != nil || len(raw) != 10+ed25519.PrivateKeySize || raw[0] != 'E' || raw[1] != 'd' {
		die(errors.New("CFBTEL_RELEASE_KEY is missing or not a releasesign private key"))
	}
	var list bytes.Buffer
	for _, f := range files {
		h, err := hashFile(f)
		if err != nil {
			die(err)
		}
		fmt.Fprintf(&list, "%s  %s\n", h, filepath.Base(f))
	}
	sig := ed25519.Sign(ed25519.PrivateKey(raw[10:]), list.Bytes())
	body := "untrusted comment: CFBTEL VPN release\n" + base64.StdEncoding.EncodeToString(append(append([]byte{}, raw[:10]...), sig...)) + "\n" + list.String()
	if err := os.WriteFile(out, []byte(body), 0o644); err != nil {
		die(err)
	}
}

func verify(sigPath, pub64 string) {
	pub, err := base64.StdEncoding.DecodeString(pub64)
	if err != nil || len(pub) != 10+ed25519.PublicKeySize {
		die(errors.New("bad public key"))
	}
	data, err := os.ReadFile(sigPath)
	if err != nil {
		die(err)
	}
	lines := bytes.SplitN(data, []byte{'\n'}, 3)
	if len(lines) != 3 {
		die(errors.New("bad signature file"))
	}
	sig, err := base64.StdEncoding.DecodeString(string(lines[1]))
	if err != nil || len(sig) != 10+ed25519.SignatureSize || !bytes.Equal(sig[:10], pub[:10]) || !ed25519.Verify(pub[10:], lines[2], sig[10:]) {
		die(errors.New("signature does not verify"))
	}
	fmt.Print(string(lines[2]))
}

func pubkey() {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(os.Getenv("CFBTEL_RELEASE_KEY")))
	if err != nil || len(raw) != 10+ed25519.PrivateKeySize || raw[0] != 'E' || raw[1] != 'd' {
		die(errors.New("CFBTEL_RELEASE_KEY is missing or not a releasesign private key"))
	}
	pub := ed25519.PrivateKey(raw[10:]).Public().(ed25519.PublicKey)
	fmt.Println(base64.StdEncoding.EncodeToString(append(append([]byte{}, raw[:10]...), pub...)))
}

func main() {
	if len(os.Args) == 2 && os.Args[1] == "pubkey" {
		pubkey()
		return
	}
	if len(os.Args) == 1 && runtime.GOOS == "windows" {
		keygenClipboard() // double-clicked
		return
	}
	if len(os.Args) >= 2 && os.Args[1] == "keygen" {
		keygen()
		return
	}
	if len(os.Args) >= 4 && os.Args[1] == "sign" {
		sign(os.Args[2], os.Args[3:])
		return
	}
	if len(os.Args) == 4 && os.Args[1] == "verify" {
		verify(os.Args[2], os.Args[3])
		return
	}
	fmt.Fprintln(os.Stderr, "usage: releasesign keygen | sign OUT FILE... | verify SIG PUBKEY")
	os.Exit(2)
}
