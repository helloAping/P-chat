// Command pchat-updatesign produces base64 ed25519 signatures for P-Chat
// update artifacts. Release operators pass the private key via
// PCHAT_UPDATE_SIGNING_KEY (or --key). The matching public key is embedded in
// internal/update.
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/p-chat/pchat/internal/update"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "genkey" {
		if err := genKey(); err != nil {
			fail(err)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "verify" {
		if err := verifySignature(os.Args[2:]); err != nil {
			fail(err)
		}
		return
	}

	fs := flag.NewFlagSet("pchat-updatesign", flag.ExitOnError)
	key := fs.String("key", "", "base64 ed25519 private key (default: PCHAT_UPDATE_SIGNING_KEY)")
	sha256Hex := fs.String("sha256", "", "artifact SHA-256 hex")
	version := fs.String("version", "", "release version")
	kind := fs.String("kind", "patch", "artifact kind: full|patch")
	_ = fs.Parse(os.Args[1:])

	privRaw := strings.TrimSpace(*key)
	if privRaw == "" {
		privRaw = strings.TrimSpace(os.Getenv("PCHAT_UPDATE_SIGNING_KEY"))
	}
	if privRaw == "" {
		fail(fmt.Errorf("--key or PCHAT_UPDATE_SIGNING_KEY is required"))
	}
	if strings.TrimSpace(*sha256Hex) == "" || strings.TrimSpace(*version) == "" {
		fail(fmt.Errorf("--sha256 and --version are required"))
	}
	priv, err := update.ParsePrivateKeyBase64(privRaw)
	if err != nil {
		fail(err)
	}
	sig, err := update.SignArtifact(priv, *sha256Hex, *version, *kind)
	if err != nil {
		fail(err)
	}
	fmt.Println(sig)
}

// verifySignature checks a signature against the embedded production public key.
func verifySignature(args []string) error {
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	sha256Hex := fs.String("sha256", "", "artifact SHA-256 hex")
	version := fs.String("version", "", "release version")
	kind := fs.String("kind", "", "artifact kind: full|patch")
	signature := fs.String("signature", "", "base64 ed25519 signature")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*sha256Hex) == "" || strings.TrimSpace(*version) == "" || strings.TrimSpace(*kind) == "" || strings.TrimSpace(*signature) == "" {
		return fmt.Errorf("--sha256, --version, --kind, and --signature are required")
	}
	pub, err := update.ParsePublicKeyBase64(update.DefaultUpdatePublicKeyBase64)
	if err != nil {
		return err
	}
	return update.VerifyArtifactSignature(pub, *sha256Hex, *version, *kind, *signature)
}

func genKey() error {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	fmt.Printf("public=%s\n", base64.StdEncoding.EncodeToString(pub))
	fmt.Printf("private=%s\n", base64.StdEncoding.EncodeToString(priv))
	fmt.Fprintln(os.Stderr, "Store private offline. Embed public in internal/update.DefaultUpdatePublicKeyBase64.")
	return nil
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, err.Error())
	os.Exit(1)
}
