package update

import (
	"bufio"
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/x509"
	_ "embed" // 内嵌发布公钥
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// releasePublicKeyPEM 发布签名公钥（ed25519，PKIX PEM）。私钥只存在于 GitHub Secrets `RELEASE_SIGNING_KEY`，
// 发布流水线用它对 SHA256SUMS 签名；流水线同时用本文件验一遍，保证公私钥成对。
//
//go:embed release-signing.pub.pem
var releasePublicKeyPEM []byte

// SumsName / SigName Release 里的校验文件与签名文件名。
const (
	SumsName = "SHA256SUMS"
	SigName  = "SHA256SUMS.sig"
)

// 签名与校验错误。
var (
	ErrBadSignature = errors.New("SHA256SUMS signature verification failed")
	ErrSumNotFound  = errors.New("file not listed in SHA256SUMS")
	ErrSumMismatch  = errors.New("sha256 mismatch")
)

// ReleasePublicKey 返回内嵌的发布公钥。
func ReleasePublicKey() (ed25519.PublicKey, error) { return ParsePublicKey(releasePublicKeyPEM) }

// ParsePublicKey 解析 PKIX PEM 格式的 ed25519 公钥。
func ParsePublicKey(data []byte) (ed25519.PublicKey, error) {
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, errors.New("public key: no PEM block")
	}
	k, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("public key: %w", err)
	}
	pub, ok := k.(ed25519.PublicKey)
	if !ok {
		return nil, fmt.Errorf("public key: want ed25519, got %T", k)
	}
	return pub, nil
}

// VerifySums 验证 SHA256SUMS 的 ed25519 签名（64 字节原始签名，openssl pkeyutl -sign -rawin 的输出）。
func VerifySums(pub ed25519.PublicKey, sums, sig []byte) error {
	if len(pub) != ed25519.PublicKeySize || len(sig) != ed25519.SignatureSize || !ed25519.Verify(pub, sums, sig) {
		return ErrBadSignature
	}
	return nil
}

// sumEntries 解析 sha256sum 格式：每行「<64 位十六进制>  <文件名>」，文件名前可带 * （二进制模式）。
func sumEntries(sums []byte) map[string]string {
	out := map[string]string{}
	sc := bufio.NewScanner(bytes.NewReader(sums))
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) != 2 || len(fields[0]) != sha256.Size*2 {
			continue
		}
		if _, err := hex.DecodeString(fields[0]); err != nil {
			continue
		}
		out[strings.TrimPrefix(fields[1], "*")] = strings.ToLower(fields[0])
	}
	return out
}

// FindSum 在 SHA256SUMS 里查某个文件的哈希。
func FindSum(sums []byte, name string) (string, error) {
	if h, ok := sumEntries(sums)[name]; ok {
		return h, nil
	}
	return "", fmt.Errorf("%w: %s", ErrSumNotFound, name)
}

// PlatformPackage 从 SHA256SUMS 里找出本平台的安装包。安装包名与版本都取自这份已验签的清单，
// 不取自下载目录里的文件名或请求标记：root 侧只信签名覆盖到的内容。
func PlatformPackage(sums []byte, goos, goarch string) (name, sum, ver string, err error) {
	suffix := "-" + goos + "-" + goarch + ".tar.gz"
	const prefix = "action-backup-"
	for n, h := range sumEntries(sums) {
		if !strings.HasPrefix(n, prefix) || !strings.HasSuffix(n, suffix) || strings.ContainsAny(n, "/\\") {
			continue
		}
		v := strings.TrimSuffix(strings.TrimPrefix(n, prefix), suffix)
		if canonical(v) == "" {
			continue
		}
		if name != "" {
			return "", "", "", fmt.Errorf("SHA256SUMS lists more than one %s-%s package", goos, goarch)
		}
		name, sum, ver = n, h, v
	}
	if name == "" {
		return "", "", "", fmt.Errorf("%w: no %s-%s package", ErrSumNotFound, goos, goarch)
	}
	return name, sum, ver, nil
}

// FileSHA256 计算文件的 SHA-256（十六进制小写）。
func FileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// VerifyFile 核对文件哈希与期望值。
func VerifyFile(path, want string) error {
	got, err := FileSHA256(path)
	if err != nil {
		return err
	}
	if !strings.EqualFold(got, want) {
		return fmt.Errorf("%w: %s", ErrSumMismatch, path)
	}
	return nil
}
