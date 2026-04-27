package main

import (
	"flag"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/scaleway/scaleway-sdk-go/api/applesilicon/v1alpha1"
	"golang.org/x/crypto/ssh"
)

// nestingVersion is the version of the GitLab nesting daemon to install.
const nestingVersion = "v0.5.0"

// bootstrapScript is a template for the script run on the Mac mini as root.
// %s is replaced with the sudo password fetched from the Scaleway API so that
// the initial privilege escalation works before the NOPASSWD rule is in place.
// It:
//  1. Grants m1 passwordless sudo (idempotent)
//  2. Installs Homebrew and Tart (via Homebrew)
//  3. Downloads the nesting daemon binary from GitLab releases
//  4. Installs a LaunchDaemon plist to start nesting at boot and starts it
const bootstrapScriptTmpl = `#!/bin/bash
set -euo pipefail

# Escalate to root using the Scaleway-provided sudo password.
if [ "$(id -u)" != "0" ]; then
  exec echo %q | sudo -S bash "$0" "$@"
fi

NESTING_VERSION="` + nestingVersion + `"
NESTING_URL="https://gitlab.com/gitlab-org/fleeting/nesting/-/releases/${NESTING_VERSION}/downloads/nesting-darwin-arm64"
NESTING_BIN="/opt/homebrew/bin/nesting"
NESTING_PLIST="/Library/LaunchDaemons/com.gitlab.fleeting.nesting.plist"

echo "==> Granting passwordless sudo to m1"
echo 'm1 ALL=(ALL) NOPASSWD: ALL' > /etc/sudoers.d/m1-nopasswd
chmod 440 /etc/sudoers.d/m1-nopasswd

echo "==> Installing Homebrew and Tart as m1"
sudo -u m1 bash -lc '
  set -euo pipefail
  export HOME=/Users/m1

  if ! command -v /opt/homebrew/bin/brew &>/dev/null; then
    echo "==> Installing Homebrew"
    NONINTERACTIVE=1 /bin/bash -c "$(curl -fsSL https://raw.githubusercontent.com/Homebrew/install/HEAD/install.sh)"
  else
    echo "==> Homebrew already installed"
  fi

  eval "$(/opt/homebrew/bin/brew shellenv)"

  echo "==> Installing Tart"
  brew install cirruslabs/cli/tart || brew upgrade cirruslabs/cli/tart
'

echo "==> Installing nesting daemon ${NESTING_VERSION}"
if [ ! -f "${NESTING_BIN}" ]; then
  curl -fsSL "${NESTING_URL}" -o "${NESTING_BIN}"
  chmod +x "${NESTING_BIN}"
else
  echo "==> nesting already installed at ${NESTING_BIN}"
fi

echo "==> Installing nesting LaunchDaemon"
cat > "${NESTING_PLIST}" << 'PLIST'
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>com.gitlab.fleeting.nesting</string>
  <key>ProgramArguments</key>
  <array>
    <string>/usr/bin/sudo</string>
    <string>-u</string>
    <string>m1</string>
    <string>/opt/homebrew/bin/nesting</string>
    <string>serve</string>
    <string>-hypervisor</string>
    <string>tart</string>
  </array>
  <key>EnvironmentVariables</key>
  <dict>
    <key>PATH</key>
    <string>/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin</string>
    <key>HOME</key>
    <string>/Users/m1</string>
  </dict>
  <key>RunAtLoad</key>
  <true/>
  <key>KeepAlive</key>
  <true/>
  <key>StandardOutPath</key>
  <string>/var/log/nesting.log</string>
  <key>StandardErrorPath</key>
  <string>/var/log/nesting.log</string>
</dict>
</plist>
PLIST

echo "==> Starting nesting daemon"
launchctl unload "${NESTING_PLIST}" 2>/dev/null || true
launchctl load -w "${NESTING_PLIST}"

echo "==> Bootstrap complete"
`

// bootstrapScript formats bootstrapScriptTmpl with the sudo password.
func bootstrapScript(sudoPassword string) string {
	return fmt.Sprintf(bootstrapScriptTmpl, sudoPassword)
}

func cmdBootstrap(args []string) {
	fs := flag.NewFlagSet("bootstrap", flag.ExitOnError)
	var cf commonFlags
	cf.bind(fs)
	var timeout time.Duration
	var keyPath string
	fs.DurationVar(&timeout, "timeout", 10*time.Minute, "SSH connection timeout")
	fs.StringVar(&keyPath, "key", filepath.Join(os.Getenv("HOME"), ".ssh", "id_rsa"), "Path to SSH private key")
	_ = fs.Parse(args)

	ids := fs.Args()
	if len(ids) != 1 {
		fmt.Fprintln(os.Stderr, "bootstrap: exactly one server ID required")
		os.Exit(1)
	}

	_, api, zone := mustInitGroup(&cf, false)
	serverID := ids[0]

	srv, err := api.GetServer(&applesilicon.GetServerRequest{
		Zone:     zone,
		ServerID: serverID,
	})
	dieOnErr(err, "getting server")

	if srv.Status != applesilicon.ServerStatusReady {
		fmt.Fprintf(os.Stderr, "error: server %s is not ready (status: %s)\n", serverID, srv.Status)
		os.Exit(1)
	}
	if srv.IP == nil {
		fmt.Fprintln(os.Stderr, "error: server has no IP address")
		os.Exit(1)
	}
	if srv.SudoPassword == "" {
		fmt.Fprintln(os.Stderr, "error: server has no sudo_password in API response")
		os.Exit(1)
	}

	keyBytes, err := os.ReadFile(keyPath)
	dieOnErr(err, fmt.Sprintf("reading SSH key %s", keyPath))

	signer, err := ssh.ParsePrivateKey(keyBytes)
	dieOnErr(err, "parsing SSH private key")

	ip := srv.IP.String()
	username := srv.SSHUsername
	sudoPassword := srv.SudoPassword

	fmt.Printf("Bootstrapping %s (%s@%s) using key %s ...\n", serverID, username, ip, keyPath)

	if err := runBootstrap(ip, username, sudoPassword, signer, timeout); err != nil {
		fmt.Fprintf(os.Stderr, "bootstrap failed: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("Bootstrap complete.")
}

func runBootstrap(ip, username, sudoPassword string, signer ssh.Signer, timeout time.Duration) error {
	cfg := &ssh.ClientConfig{
		User: username,
		Auth: []ssh.AuthMethod{
			ssh.PublicKeys(signer),
		},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), //nolint:gosec
		Timeout:         30 * time.Second,
	}

	addr := net.JoinHostPort(ip, "22")
	fmt.Printf("Connecting to %s ...\n", addr)

	var client *ssh.Client
	deadline := time.Now().Add(timeout)
	for {
		var err error
		client, err = ssh.Dial("tcp", addr, cfg)
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out waiting for SSH: %w", err)
		}
		fmt.Printf("  waiting for SSH (%v)...\n", err)
		time.Sleep(5 * time.Second)
	}
	defer client.Close()

	fmt.Println("Connected. Uploading bootstrap script...")

	// The script self-escalates to root using the sudo password embedded in it.
	script := bootstrapScript(sudoPassword)

	// Step 1: write the script to a temp file.
	if err := runSession(client, fmt.Sprintf("cat > /tmp/bootstrap.sh << 'ENDOFSCRIPT'\n%sENDOFSCRIPT", script)); err != nil {
		return fmt.Errorf("uploading script: %w", err)
	}

	fmt.Println("Running bootstrap script (this may take 10-20 minutes)...")

	// Step 2: execute — the script handles sudo escalation internally.
	if err := runSession(client, "bash /tmp/bootstrap.sh"); err != nil {
		return fmt.Errorf("running script: %w", err)
	}

	// Step 3: clean up.
	_ = runSession(client, "rm -f /tmp/bootstrap.sh")

	return nil
}

func runSession(client *ssh.Client, cmd string) error {
	sess, err := client.NewSession()
	if err != nil {
		return fmt.Errorf("new session: %w", err)
	}
	defer sess.Close()
	sess.Stdout = os.Stdout
	sess.Stderr = os.Stderr
	return sess.Run(cmd)
}


