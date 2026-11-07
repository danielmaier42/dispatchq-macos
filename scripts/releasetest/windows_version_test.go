package releasetest

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// Exercise the complete verifier on real Go PE payloads carrying an -X version
// string. A manifest's claimed version must not turn a prefix into a match.
func TestWindowsVerifierRequiresExactVersion(t *testing.T) {
	for _, tool := range []string{"go", "jq", "shasum", "strings"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s required for the executable verifier fixture", tool)
		}
	}
	for _, arch := range []struct{ goarch, label string }{{"amd64", "x64"}, {"arm64", "arm64"}} {
		t.Run(arch.label, func(t *testing.T) {
			root := t.TempDir()
			for _, name := range []string{"arch.sh", "verify-build.sh"} {
				data, err := os.ReadFile(filepath.Join(scripts, "windows", name))
				if err != nil {
					t.Fatal(err)
				}
				writeFixture(t, filepath.Join(root, "scripts", "windows", name), data)
			}
			source := filepath.Join(root, "main.go")
			writeFixture(t, source, []byte("package main\nvar stamped = \"dev\"\nfunc main() { println(stamped) }\n"))
			stage := filepath.Join(root, "build", "windows", "stage-"+arch.label)
			if err := os.MkdirAll(stage, 0o755); err != nil {
				t.Fatal(err)
			}
			payload := filepath.Join(stage, "dispatchqd.exe")
			cmd := exec.Command("go", "build", "-trimpath", "-buildvcs=false", "-ldflags=-X main.stamped=v2.1.01", "-o", payload, source)
			cmd.Env = append(os.Environ(), "GOOS=windows", "GOARCH="+arch.goarch, "CGO_ENABLED=0")
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("build real PE fixture: %v\n%s", err, out)
			}
			data, err := os.ReadFile(payload)
			if err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"dispatchq.exe", "dispatchqapp.exe"} {
				writeFixture(t, filepath.Join(stage, name), data)
			}
			for _, helper := range []struct {
				arch    string
				machine uint16
			}{{"amd64", 0x3e}, {"arm64", 0xb7}} {
				elf := make([]byte, 64)
				copy(elf, "\x7fELF\x02\x01")
				binary.LittleEndian.PutUint16(elf[18:], helper.machine)
				writeFixture(t, filepath.Join(stage, "wsl", "dispatchq-wsl-linux-"+helper.arch), elf)
			}
			for _, version := range []string{"2.1.01", "2.1.0", "2.1", "2.1.01-beta"} {
				t.Run(version, func(t *testing.T) {
					outDir := t.TempDir()
					name := "dispatchq-" + version + "-windows-" + arch.label + "-beta.exe"
					installer := make([]byte, 256)
					copy(installer, "MZ")
					binary.LittleEndian.PutUint32(installer[60:], 128)
					copy(installer[128:], "PE\x00\x00")
					binary.LittleEndian.PutUint16(installer[132:], 0x14c)
					sum := sha256.Sum256(installer)
					digest := hex.EncodeToString(sum[:])
					writeFixture(t, filepath.Join(outDir, name), installer)
					writeFixture(t, filepath.Join(outDir, "SHA256SUMS"), []byte(digest+"  "+name+"\n"))
					machine := "0x8664"
					if arch.goarch == "arm64" {
						machine = "0xaa64"
					}
					info, err := json.Marshal(map[string]any{
						"source_head": "fixture", "source_dirty": false, "version": version,
						"target": "windows/" + arch.goarch, "payload_pe_machine": machine,
						"installer": name, "sha256": digest,
					})
					if err != nil {
						t.Fatal(err)
					}
					writeFixture(t, filepath.Join(outDir, "BUILD-INFO.json"), info)
					cmd := exec.Command("bash", filepath.Join(root, "scripts", "windows", "verify-build.sh"), arch.goarch, version, "fixture", outDir)
					out, err := cmd.CombinedOutput()
					if wantOK := version == "2.1.01"; (err == nil) != wantOK {
						t.Fatalf("claimed version %s, payload v2.1.01: err=%v\n%s", version, err, out)
					}
				})
			}
		})
	}
}

func writeFixture(t *testing.T, name string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, data, 0o644); err != nil {
		t.Fatal(err)
	}
}
