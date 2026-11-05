//go:build windows

package main

import (
	"bufio"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"
)

// The "portable Docker server" is a private WSL2 distribution (Alpine Linux +
// Docker Engine) whose virtual disk lives in the engine\ folder next to the
// exe. Nothing is installed into Windows apart from the WSL registration,
// and the engine only runs while the launcher window is open.

const (
	imageName     = "ghcr.io/vrkttr/bas-instagib-server:latest"
	containerName = "bas-instagib-server"
	dataVolume    = "bas-data"
	alpineIndex   = "https://dl-cdn.alpinelinux.org/alpine/latest-stable/releases/x86_64/latest-releases.yaml"
	alpineBase    = "https://dl-cdn.alpinelinux.org/alpine/latest-stable/releases/x86_64/"
	alpineFallbck = "https://dl-cdn.alpinelinux.org/alpine/v3.22/releases/x86_64/alpine-minirootfs-3.22.0-x86_64.tar.gz"
	provisionMark = "/etc/bas-engine-v1"
)

type Engine struct {
	Dir    string // absolute path of the engine folder
	Distro string // WSL distribution name (unique per folder)

	daemon      *exec.Cmd
	daemonStdin io.WriteCloser
	daemonDone  chan error
}

func newEngine(dir string) *Engine {
	h := sha1.Sum([]byte(strings.ToLower(dir)))
	return &Engine{Dir: dir, Distro: "BAS-Instagib-" + hex.EncodeToString(h[:])[:8]}
}

// wslCmd builds a wsl.exe command. Children get their own process group so
// Ctrl+C in the window is handled only by the launcher.
func wslCmd(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "wsl.exe", args...)
	cmd.Env = append(os.Environ(), "WSL_UTF8=1")
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
	return cmd
}

// sh runs a command inside the engine as root and returns its combined output.
func (e *Engine) sh(ctx context.Context, script string) (string, error) {
	out, err := wslCmd(ctx, "-d", e.Distro, "-u", "root", "--exec", "/bin/sh", "-c", script).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// docker runs the docker CLI inside the engine, streaming output to the console.
func (e *Engine) docker(ctx context.Context, args ...string) error {
	full := append([]string{"-d", e.Distro, "-u", "root", "--exec", "docker"}, args...)
	cmd := wslCmd(ctx, full...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func (e *Engine) dockerOut(ctx context.Context, args ...string) (string, error) {
	full := append([]string{"-d", e.Distro, "-u", "root", "--exec", "docker"}, args...)
	out, err := wslCmd(ctx, full...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func checkWSL() error {
	if _, err := exec.LookPath("wsl.exe"); err != nil {
		return errors.New("wsl.exe was not found – this Windows version does not support WSL2")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := wslCmd(ctx, "--status").CombinedOutput()
	if err != nil {
		return fmt.Errorf("WSL is not installed or not ready (%s)", firstLine(string(out)))
	}
	return nil
}

func (e *Engine) vhdx() string { return filepath.Join(e.Dir, "ext4.vhdx") }

// Ensure creates, re-registers and provisions the engine as needed.
func (e *Engine) Ensure(ctx context.Context) error {
	if err := os.MkdirAll(e.Dir, 0o755); err != nil {
		return err
	}
	if _, err := os.Stat(e.vhdx()); err != nil {
		step("Creating the portable Docker engine (one-time, needs ~300 MB)")
		tarball := filepath.Join(e.Dir, "rootfs.tar.gz")
		if err := downloadRootfs(ctx, tarball); err != nil {
			return fmt.Errorf("download of the Linux base system failed: %w", err)
		}
		// A stale registration with the same name would block the import.
		_ = wslCmd(ctx, "--unregister", e.Distro).Run()
		out, err := wslCmd(ctx, "--import", e.Distro, e.Dir, tarball, "--version", "2").CombinedOutput()
		os.Remove(tarball)
		if err != nil {
			return fmt.Errorf("wsl --import failed: %s", strings.TrimSpace(string(out)))
		}
	} else if _, err := e.sh(ctx, "true"); err != nil {
		// Folder was moved/copied to another PC: register the existing disk again.
		step("Registering the existing engine disk with WSL")
		out, err := wslCmd(ctx, "--import-in-place", e.Distro, e.vhdx()).CombinedOutput()
		if err != nil {
			return fmt.Errorf("wsl --import-in-place failed: %s", strings.TrimSpace(string(out)))
		}
	}

	if _, err := e.sh(ctx, "test -f "+provisionMark); err != nil {
		step("Installing Docker Engine into the portable engine")
		cmd := wslCmd(ctx, "-d", e.Distro, "-u", "root", "--exec", "/bin/sh", "-c",
			"apk update && apk add --no-cache docker && touch "+provisionMark)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("installing Docker failed: %w", err)
		}
	}
	return nil
}

// Start boots the engine and the Docker daemon and waits until it answers.
//
// The daemon is wrapped in a small watchdog script: it blocks on stdin, which
// is a pipe held open by this launcher. When the launcher exits for any
// reason (window closed, killed, crashed), the pipe breaks, and the script
// stops the game server and the daemon inside Linux.
func (e *Engine) Start(ctx context.Context) error {
	_ = wslCmd(ctx, "--terminate", e.Distro).Run() // start from a clean state

	script := `
mountpoint -q /sys/fs/cgroup || { mkdir -p /sys/fs/cgroup; mount -t cgroup2 cgroup2 /sys/fs/cgroup 2>/dev/null; }
rm -f /var/run/docker.pid /var/run/docker/containerd/containerd.pid
dockerd --iptables=false --ip6tables=false --bridge=none >/var/log/dockerd.log 2>&1 &
D=$!
cat >/dev/null
docker stop -t 5 ` + containerName + ` >/dev/null 2>&1
kill $D 2>/dev/null
wait $D
`
	cmd := wslCmd(context.Background(), "-d", e.Distro, "-u", "root", "--exec", "/bin/sh", "-c", script)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("could not start the engine: %w", err)
	}
	e.daemon, e.daemonStdin = cmd, stdin
	e.daemonDone = make(chan error, 1)
	go func() { e.daemonDone <- cmd.Wait() }()

	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-e.daemonDone:
			return fmt.Errorf("the engine stopped unexpectedly (%v)", err)
		case <-time.After(time.Second):
		}
		c, cancel := context.WithTimeout(ctx, 10*time.Second)
		_, err := e.dockerOut(c, "info", "--format", "{{.ServerVersion}}")
		cancel()
		if err == nil {
			return nil
		}
	}
	log, _ := e.sh(ctx, "tail -n 20 /var/log/dockerd.log")
	return fmt.Errorf("Docker daemon did not start within 90 s. Last log lines:\n%s", log)
}

// PullFresh removes the locally cached image and downloads it again, so every
// start runs the newest official image (no cache).
func (e *Engine) PullFresh(ctx context.Context) error {
	_, _ = e.dockerOut(ctx, "rm", "-f", containerName)
	_, _ = e.dockerOut(ctx, "image", "rm", "-f", imageName)
	_, _ = e.dockerOut(ctx, "image", "prune", "-f")
	return e.docker(ctx, "pull", imageName)
}

func (e *Engine) RunServer(ctx context.Context, c Config, mapsDir string) error {
	args := []string{"run", "-d",
		"--name", containerName,
		"--network", "host",
		"--read-only",
		"--cap-drop", "ALL",
		"--security-opt", "no-new-privileges:true",
		"--tmpfs", "/tmp",
		"--stop-timeout", "10",
		"--log-opt", "max-size=10m", "--log-opt", "max-file=5",
		"-e", "BAS_SERVER_NAME=" + c.ServerName,
		"-e", "BAS_MODE=" + c.Mode,
		"-e", "BAS_PASSWORD=" + c.Password,
		"-e", "BAS_MAPS=" + c.Maps,
		"-e", fmt.Sprintf("BAS_MAX_PLAYERS=%d", c.MaxPlayers),
		"-e", fmt.Sprintf("BAS_UDP_PORT=%d", c.Port),
		"-e", "BAS_PUBLIC_IP=" + c.PublicIP,
		"-v", dataVolume + ":/app/data",
	}
	if p, ok := toWSLPath(mapsDir); ok {
		args = append(args, "-v", p+":/app/maps:ro")
	} else {
		warn("The maps folder is not on a local drive – custom maps are disabled.")
	}
	args = append(args, imageName)
	out, err := e.dockerOut(ctx, args...)
	if err != nil {
		return fmt.Errorf("docker run failed: %s", out)
	}
	return nil
}

// Logs streams the server log until the container stops or ctx is cancelled.
func (e *Engine) Logs(ctx context.Context) error {
	return e.docker(ctx, "logs", "-f", containerName)
}

func (e *Engine) ExitCode() string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, _ := e.dockerOut(ctx, "inspect", "-f", "{{.State.ExitCode}}", containerName)
	return out
}

// IP returns the engine's IPv4 address on the WSL virtual network.
func (e *Engine) IP(ctx context.Context) (string, error) {
	out, err := e.sh(ctx, "ip -4 -o addr show eth0")
	if err != nil {
		return "", fmt.Errorf("%v: %s", err, out)
	}
	m := regexp.MustCompile(`inet (\d+\.\d+\.\d+\.\d+)/`).FindStringSubmatch(out)
	if m == nil {
		return "", fmt.Errorf("no IPv4 address on eth0: %s", out)
	}
	return m[1], nil
}

// Stop shuts everything down within the ~5 s Windows grants after the
// console window was closed.
func (e *Engine) Stop() {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	_, _ = e.dockerOut(ctx, "stop", "-t", "2", containerName)
	cancel()
	if e.daemonStdin != nil {
		e.daemonStdin.Close()
	}
	ctx2, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	_ = wslCmd(ctx2, "--terminate", e.Distro).Run()
	cancel2()
}

func toWSLPath(p string) (string, bool) {
	p = filepath.Clean(p)
	if len(p) < 3 || p[1] != ':' || p[2] != '\\' {
		return "", false
	}
	drive := strings.ToLower(p[:1])
	return "/mnt/" + drive + "/" + strings.ReplaceAll(p[3:], `\`, "/"), true
}

func downloadRootfs(ctx context.Context, dst string) error {
	url := alpineFallbck
	if body, err := httpGetSmall(ctx, alpineIndex); err == nil {
		re := regexp.MustCompile(`file:\s*"?(alpine-minirootfs-[0-9.]+-x86_64\.tar\.gz)`)
		if m := re.FindStringSubmatch(body); m != nil {
			url = alpineBase + m[1]
		}
	}
	fmt.Printf("  Downloading %s\n", url)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %s", resp.Status)
	}
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	_, err = io.Copy(f, resp.Body)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

func httpGetSmall(ctx context.Context, url string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %s", resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return string(b), err
}

// wslMirrored reports whether WSL runs in mirrored networking mode, in which
// the engine shares the Windows network and needs no UDP relay.
func wslMirrored() bool {
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	f, err := os.Open(filepath.Join(home, ".wslconfig"))
	if err != nil {
		return false
	}
	defer f.Close()
	re := regexp.MustCompile(`(?i)^\s*networkingMode\s*=\s*mirrored\s*$`)
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if re.MatchString(sc.Text()) {
			return true
		}
	}
	return false
}

func firstLine(s string) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\x00", ""))
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}
