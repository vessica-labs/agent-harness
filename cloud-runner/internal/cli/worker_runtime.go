package cli

import "strings"

// The checkpoint runs the linux-amd64 worker binary. Keep the runtime archive
// immutable too; verify a new version against nodejs.org's SHASUMS256.txt.
const workerNodeVersion = "24.22.1"
const workerNodeSHA256 = "6c172a9ab6718f7b801fb5a7bd486af2ee5e32758086a04b84fcce2aa77095e6"

func workerShellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}

func workerNodeInstallCommand(version, digest, prefix string) string {
	url := "https://nodejs.org/dist/v" + version + "/node-v" + version + "-linux-x64.tar.xz"
	return strings.Join([]string{
		"set -eu",
		`test "$(uname -sm)" = 'Linux x86_64'`,
		`work=$(mktemp -d)`,
		`trap 'rm -rf "$work"' EXIT`,
		`curl --fail --silent --show-error --location --proto '=https' --tlsv1.2 --output "$work/node.tar.xz" ` + workerShellQuote(url),
		`printf '%s  %s\n' ` + workerShellQuote(digest) + ` "$work/node.tar.xz" | sha256sum -c -`,
		"mkdir -p " + workerShellQuote(prefix),
		`tar -xJf "$work/node.tar.xz" -C ` + workerShellQuote(prefix) + " --strip-components=1",
		`test "$( ` + workerShellQuote(prefix+"/bin/node") + ` --version)" = ` + workerShellQuote("v"+version),
	}, " && ")
}
