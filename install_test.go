package main

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestUnixInstaller(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows installer is tested by scripts/test-install.ps1")
	}
	for _, corrupt := range []bool{false, true} {
		t.Run(fmt.Sprintf("corrupt=%v", corrupt), func(t *testing.T) {
			dir := t.TempDir()
			fixtures := filepath.Join(dir, "fixtures")
			fakeBin := filepath.Join(dir, "fake-bin")
			userDir := filepath.Join(dir, "user")
			installDir := filepath.Join(userDir, "It's a space", "bin")
			for _, path := range []string{fixtures, fakeBin, userDir} {
				if err := os.MkdirAll(path, 0700); err != nil {
					t.Fatal(err)
				}
			}
			archive, err := os.Create(filepath.Join(fixtures, "archive.tar.gz"))
			if err != nil {
				t.Fatal(err)
			}
			gz := gzip.NewWriter(archive)
			tw := tar.NewWriter(gz)
			binary := []byte("#!/bin/sh\nprintf 'nnun version 0.1.0\\n'\n")
			if err := tw.WriteHeader(&tar.Header{Name: "nnun", Mode: 0755, Size: int64(len(binary))}); err != nil {
				t.Fatal(err)
			}
			_, _ = tw.Write(binary)
			_ = tw.Close()
			_ = gz.Close()
			_ = archive.Close()
			b, _ := os.ReadFile(filepath.Join(fixtures, "archive.tar.gz"))
			sum := fmt.Sprintf("%x", sha256.Sum256(b))
			if corrupt {
				sum = strings.Repeat("0", 64)
			}
			asset := fmt.Sprintf("nnun_0.1.0_%s_%s.tar.gz", runtime.GOOS, runtime.GOARCH)
			_ = os.WriteFile(filepath.Join(fixtures, "checksums.txt"), []byte(sum+"  "+asset+"\n"), 0600)
			curl := `#!/bin/sh
target=
destination=
while [ "$#" -gt 0 ]; do
  case "$1" in
    -o) destination=$2; shift 2 ;;
    --retry|--proto|--proto-redir|-w) shift 2 ;;
    -*) shift ;;
    *) target=$1; shift ;;
  esac
done
case "$target" in
  */latest) printf 'https://github.com/Savokr/nnun-cli/releases/tag/v0.1.0' ;;
  */download/v0.1.0/checksums.txt) cp "$NNUN_FIXTURES/checksums.txt" "$destination" ;;
  */download/v0.1.0/*.tar.gz) cp "$NNUN_FIXTURES/archive.tar.gz" "$destination" ;;
  *) exit 91 ;;
esac
`
			_ = os.WriteFile(filepath.Join(fakeBin, "curl"), []byte(curl), 0700)
			env := []string{}
			for _, v := range os.Environ() {
				if !strings.HasPrefix(v, "HOME=") && !strings.HasPrefix(v, "PATH=") && !strings.HasPrefix(v, "SHELL=") && !strings.HasPrefix(v, "NNUN_") {
					env = append(env, v)
				}
			}
			env = append(env, "HOME="+userDir, "PATH="+fakeBin+":"+os.Getenv("PATH"), "SHELL=/bin/zsh", "NNUN_INSTALL_DIR="+installDir, "NNUN_FIXTURES="+fixtures)
			run := func(args ...string) ([]byte, error) {
				cmd := exec.Command("sh", append([]string{"install.sh"}, args...)...)
				cmd.Env = env
				return cmd.CombinedOutput()
			}
			out, err := run()
			if corrupt {
				if err == nil || !strings.Contains(string(out), "Checksum mismatch") {
					t.Fatalf("%s %v", out, err)
				}
				if _, err := os.Stat(filepath.Join(installDir, "nnun")); !os.IsNotExist(err) {
					t.Fatal("corrupt binary installed")
				}
				return
			}
			if err != nil {
				t.Fatalf("%s %v", out, err)
			}
			out, err = run("--version", "v0.1.0")
			if err != nil {
				t.Fatalf("%s %v", out, err)
			}
			profile, _ := os.ReadFile(filepath.Join(userDir, ".zshrc"))
			if strings.Count(string(profile), "# Nnun CLI") != 1 {
				t.Fatalf("PATH is not idempotent: %s", profile)
			}
			cmd := exec.Command("sh", "-c", `. "$HOME/.zshrc"; printf '%s' "$PATH"`)
			cmd.Env = env
			out, err = cmd.CombinedOutput()
			if err != nil || !strings.HasPrefix(string(out), installDir+":") {
				t.Fatalf("unsafe PATH quoting: %s %v", out, err)
			}
		})
	}
}
