package update

import (
	"bytes"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/ssh"
)

// ErrBadSignature means checksums.txt.sig isn't a valid signature by the
// frost release key over checksums.txt.
var ErrBadSignature = errors.New("checksums.txt isn't signed by the frost release key")

// sigNamespace is what release.sh signs with (ssh-keygen -Y sign -n file).
const sigNamespace = "file"

// verifySSHSig checks an armored `ssh-keygen -Y sign` signature over msg,
// made by exactly the key in authorized (one authorized_keys line). The
// format is OpenSSH's PROTOCOL.sshsig.
func verifySSHSig(authorized string, msg, armored []byte) error {
	want, _, _, _, err := ssh.ParseAuthorizedKey([]byte(authorized))
	if err != nil {
		return fmt.Errorf("release key: %w", err)
	}
	blob, err := dearmor(armored)
	if err != nil {
		return err
	}
	var sig struct {
		Version   uint32
		PublicKey []byte
		Namespace string
		Reserved  []byte
		HashAlg   string
		Signature []byte
	}
	body, ok := bytes.CutPrefix(blob, []byte("SSHSIG"))
	if !ok {
		return fmt.Errorf("%w (not an SSH signature)", ErrBadSignature)
	}
	if err := ssh.Unmarshal(body, &sig); err != nil {
		return fmt.Errorf("%w (%v)", ErrBadSignature, err)
	}
	if sig.Version != 1 {
		return fmt.Errorf("%w (signature version %d)", ErrBadSignature, sig.Version)
	}
	if !bytes.Equal(sig.PublicKey, want.Marshal()) {
		return fmt.Errorf("%w (signed by a different key)", ErrBadSignature)
	}
	if sig.Namespace != sigNamespace {
		return fmt.Errorf("%w (namespace %q)", ErrBadSignature, sig.Namespace)
	}
	var digest []byte
	switch sig.HashAlg {
	case "sha512":
		h := sha512.Sum512(msg)
		digest = h[:]
	case "sha256":
		h := sha256.Sum256(msg)
		digest = h[:]
	default:
		return fmt.Errorf("%w (hash %q)", ErrBadSignature, sig.HashAlg)
	}
	var inner ssh.Signature
	if err := ssh.Unmarshal(sig.Signature, &inner); err != nil {
		return fmt.Errorf("%w (%v)", ErrBadSignature, err)
	}
	signed := append([]byte("SSHSIG"), ssh.Marshal(struct {
		Namespace string
		Reserved  []byte
		HashAlg   string
		Hash      []byte
	}{sig.Namespace, sig.Reserved, sig.HashAlg, digest})...)
	if err := want.Verify(signed, &inner); err != nil {
		return ErrBadSignature
	}
	return nil
}

// dearmor strips the BEGIN/END lines and decodes the base64 between them.
func dearmor(armored []byte) ([]byte, error) {
	const begin, end = "-----BEGIN SSH SIGNATURE-----", "-----END SSH SIGNATURE-----"
	s := strings.TrimSpace(strings.ReplaceAll(string(armored), "\r\n", "\n"))
	s, ok := strings.CutPrefix(s, begin)
	if !ok {
		return nil, fmt.Errorf("%w (not an SSH signature)", ErrBadSignature)
	}
	s, ok = strings.CutSuffix(s, end)
	if !ok {
		return nil, fmt.Errorf("%w (truncated)", ErrBadSignature)
	}
	b, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(s), ""))
	if err != nil {
		return nil, fmt.Errorf("%w (%v)", ErrBadSignature, err)
	}
	return b, nil
}
