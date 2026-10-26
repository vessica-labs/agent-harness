package cli

import (
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestWorkerNodeInstallChecksArchiveBeforeExtracting(t *testing.T) {
	for _, valid := range []bool{false, true} {
		t.Run(fmt.Sprint(valid), func(t *testing.T) {
			root := t.TempDir()
			bin := filepath.Join(root, "bin")
			prefix := filepath.Join(root, "runtime")
			if err := os.Mkdir(bin, 0755); err != nil {
				t.Fatal(err)
			}
			payload := []byte("synthetic worker runtime archive\n")
			digest := fmt.Sprintf("%x", sha256.Sum256(payload))
			if !valid {
				digest = strings.Repeat("0", 64)
			}
			fixture := filepath.Join(root, "archive")
			if err := os.WriteFile(fixture, payload, 0600); err != nil {
				t.Fatal(err)
			}
			writeTool := func(name, body string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\nset -eu\n"+body), 0755); err != nil {
					t.Fatal(err)
				}
			}
			writeTool("uname", "printf 'Linux x86_64\\n'\n")
			writeTool("curl", "while test $# -gt 0; do if test \"$1\" = --output; then cp "+workerShellQuote(fixture)+" \"$2\"; exit; fi; shift; done; exit 1\n")
			writeTool("tar", "mkdir -p "+workerShellQuote(prefix+"/bin")+"; printf '#!/bin/sh\\nprintf v24.22.1\\n' > "+workerShellQuote(prefix+"/bin/node")+"; chmod 0755 "+workerShellQuote(prefix+"/bin/node")+"\n")
			if _, err := exec.LookPath("sha256sum"); err != nil {
				shasum, err := exec.LookPath("shasum")
				if err != nil {
					t.Fatal(err)
				}
				writeTool("sha256sum", "exec "+workerShellQuote(shasum)+" -a 256 \"$@\"\n")
			}
			command := exec.Command("sh", "-c", workerNodeInstallCommand(workerNodeVersion, digest, prefix))
			command.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			output, err := command.CombinedOutput()
			if valid && err != nil {
				t.Fatalf("valid archive failed: %v\n%s", err, output)
			}
			if !valid && err == nil {
				t.Fatal("modified archive accepted")
			}
			_, installed := os.Stat(prefix + "/bin/node")
			if (installed == nil) != valid {
				t.Fatal("archive extraction crossed the checksum barrier")
			}
		})
	}
}

func TestWorkerNodePinnedVersionAndChecksum(t *testing.T) {
	if workerNodeVersion != "24.22.1" || workerNodeSHA256 != "6c172a9ab6718f7b801fb5a7bd486af2ee5e32758086a04b84fcce2aa77095e6" {
		t.Fatal("worker security baseline changed; review upstream checksum and runtime gate")
	}
	commands := railwayUpgradeCommands("v0.1.0-rc.45")
	if strings.Contains(strings.Join(commands, "\n"), "setup_24.x") {
		t.Fatal("floating Node installer retained")
	}
	for _, command := range commands {
		if output, err := exec.Command("sh", "-n", "-c", command).CombinedOutput(); err != nil {
			t.Fatalf("invalid checkpoint command: %v\n%s", err, output)
		}
	}
}

func TestWorkerNodeInstallRejectsOtherArchitecturesBeforeDownload(t *testing.T) {
	root := t.TempDir()
	marker := filepath.Join(root, "download-attempted")
	for name, body := range map[string]string{
		"uname": "#!/bin/sh\nprintf 'Linux aarch64\\n'\n",
		"curl":  "#!/bin/sh\ntouch " + workerShellQuote(marker) + "\nexit 99\n",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0755); err != nil {
			t.Fatal(err)
		}
	}
	command := exec.Command("sh", "-c", workerNodeInstallCommand(workerNodeVersion, workerNodeSHA256, filepath.Join(root, "runtime")))
	command.Env = append(os.Environ(), "PATH="+root+string(os.PathListSeparator)+os.Getenv("PATH"))
	if output, err := command.CombinedOutput(); err == nil {
		t.Fatalf("unsupported architecture accepted: %s", output)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("unsupported architecture attempted a download")
	}
}

// CI supplies the official archive explicitly; normal unit tests never download
// software or require a Linux execution environment.
func TestWorkerNodeInstallOfficialArchive(t *testing.T) {
	archive := os.Getenv("HARNESS_TEST_NODE_ARCHIVE")
	if archive == "" {
		t.Skip("official archive not supplied")
	}
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		t.Fatal("official archive smoke requires linux/amd64")
	}
	if _, err := os.Stat(archive); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	bin := filepath.Join(root, "transport")
	prefix := filepath.Join(root, "runtime")
	if err := os.Mkdir(bin, 0755); err != nil {
		t.Fatal(err)
	}
	// Exercise the production checksum and extraction command with an offline
	// download transport. tar, sha256sum and the resulting Node are real binaries.
	body := "#!/bin/sh\nset -eu\nwhile test $# -gt 0; do if test \"$1\" = --output; then cp " + workerShellQuote(archive) + " \"$2\"; exit; fi; shift; done; exit 1\n"
	if err := os.WriteFile(filepath.Join(bin, "curl"), []byte(body), 0755); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("sh", "-c", workerNodeInstallCommand(workerNodeVersion, workerNodeSHA256, prefix))
	command.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"))
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("official Node install failed: %v\n%s", err, output)
	}
}
