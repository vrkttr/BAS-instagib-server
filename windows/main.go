//go:build windows

// BAS-Instagib-Server.exe – starts a portable Docker engine (WSL2), pulls a
// fresh copy of the official BAS: Instagib server image and runs it. Closing
// the window stops the server and the engine. Settings are kept in
// config.env next to the exe.
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var version = "dev"

var stdinLines = make(chan string)

func main() {
	setup := flag.Bool("setup", false, "edit the server settings before starting")
	flag.Parse()

	setConsoleTitle("BAS: Instagib Server")
	killChildrenWithUs()

	go func() {
		sc := bufio.NewScanner(os.Stdin)
		for sc.Scan() {
			stdinLines <- sc.Text()
		}
		close(stdinLines)
	}()

	fmt.Printf("BAS: Instagib – Windows server launcher %s\n", version)
	fmt.Println("Closing this window stops the server.")
	fmt.Println()

	if err := run(*setup); err != nil {
		fmt.Println()
		fmt.Println("ERROR:", err)
		pause()
		os.Exit(1)
	}
}

func run(forceSetup bool) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if p, err := filepath.EvalSymlinks(exe); err == nil {
		exe = p
	}
	baseDir := filepath.Dir(exe)
	cfgPath := filepath.Join(baseDir, "config.env")
	mapsDir := filepath.Join(baseDir, "maps")
	_ = os.MkdirAll(mapsDir, 0o755)

	// --- configuration -----------------------------------------------------
	cfg, err := loadConfig(cfgPath)
	switch {
	case errors.Is(err, os.ErrNotExist):
		fmt.Println("No configuration found – let's set up your server.")
		cfg = runSetup(defaultConfig())
		if err := saveConfig(cfgPath, cfg); err != nil {
			return fmt.Errorf("could not save %s: %w", cfgPath, err)
		}
		fmt.Printf("Settings saved to %s\n", cfgPath)
	case err != nil:
		return fmt.Errorf("could not read %s: %w", cfgPath, err)
	default:
		fmt.Println("Current settings (" + cfgPath + "):")
		cfg.print()
		edit := forceSetup || cfg.validate() != nil
		if err := cfg.validate(); err != nil {
			fmt.Printf("\n!! %v\n", err)
		}
		if !edit {
			fmt.Print("\nType E + Enter within 10 seconds to change the settings, or just Enter to start: ")
			select {
			case l := <-stdinLines:
				edit = strings.EqualFold(strings.TrimSpace(l), "e")
			case <-time.After(10 * time.Second):
				fmt.Println()
			}
		}
		if edit {
			cfg = runSetup(cfg)
			if err := saveConfig(cfgPath, cfg); err != nil {
				return fmt.Errorf("could not save %s: %w", cfgPath, err)
			}
			fmt.Printf("Settings saved to %s\n", cfgPath)
		}
	}

	// --- shutdown handling --------------------------------------------------
	// Closing the console window arrives as SIGTERM; Windows then gives us
	// about 5 seconds to clean up.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)

	engine := newEngine(filepath.Join(baseDir, "engine"))
	var relay *UDPRelay
	var once sync.Once
	shutdown := func() {
		once.Do(func() {
			fmt.Println("\nStopping the server …")
			if relay != nil {
				relay.Close()
			}
			engine.Stop()
			fmt.Println("Server stopped.")
		})
	}
	defer shutdown()
	go func() {
		<-sig
		cancel()
		shutdown()
		os.Exit(0)
	}()

	// --- engine ---------------------------------------------------------------
	step("Checking WSL2")
	if err := checkWSL(); err != nil {
		offerWSLInstall()
		return err
	}
	if err := engine.Ensure(ctx); err != nil {
		return err
	}
	step("Starting the portable Docker engine")
	if err := engine.Start(ctx); err != nil {
		return err
	}

	step("Pulling a fresh server image (no cache): " + imageName)
	if err := engine.PullFresh(ctx); err != nil {
		return fmt.Errorf("pulling the server image failed: %w", err)
	}

	step("Starting the game server")
	if err := engine.RunServer(ctx, cfg, mapsDir); err != nil {
		return err
	}

	if wslMirrored() {
		fmt.Println("  WSL mirrored networking detected – the server uses the Windows network directly.")
	} else {
		ip, err := engine.IP(ctx)
		if err != nil {
			return fmt.Errorf("could not determine the engine address: %w", err)
		}
		relay, err = startRelay(cfg.Port, ip)
		if err != nil {
			return fmt.Errorf("could not open UDP port %d on Windows (already in use?): %w", cfg.Port, err)
		}
		fmt.Printf("  UDP port %d on Windows is forwarded to the server.\n", cfg.Port)
		fmt.Println("  If Windows asks about the firewall, allow access (private AND public networks).")
	}

	step("Server log (close this window to stop the server)")
	fmt.Println()
	_ = engine.Logs(ctx)
	if ctx.Err() != nil {
		select {} // shutdown goroutine exits the process
	}
	fmt.Printf("\nThe server has stopped (exit code %s).\n", engine.ExitCode())
	shutdown()
	pause()
	return nil
}

func step(s string) { fmt.Printf("\n>> %s\n", s) }

func warn(s string) { fmt.Printf("  WARNING: %s\n", s) }

func pause() {
	fmt.Print("\nPress Enter to close this window.")
	<-stdinLines
}

func offerWSLInstall() {
	fmt.Println()
	fmt.Println("The portable Docker engine needs WSL2 (built into Windows 10 2004+ and Windows 11).")
	fmt.Println("It can be enabled once with:  wsl --install --no-distribution   (as administrator, then reboot)")
	fmt.Print("Type Y + Enter to run this now (an administrator prompt will appear): ")
	l := <-stdinLines
	if !strings.EqualFold(strings.TrimSpace(l), "y") {
		return
	}
	cmd := exec.Command("powershell.exe", "-NoProfile", "-Command",
		"Start-Process -FilePath wsl.exe -ArgumentList '--install','--no-distribution' -Verb RunAs -Wait")
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Println("  Could not start the installation:", err)
		return
	}
	fmt.Println("  Done. Please restart Windows and start this program again.")
}

// killChildrenWithUs puts the launcher into a job object that kills all child
// processes (wsl.exe) when the launcher exits, even if it is killed hard.
func killChildrenWithUs() {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		windows.CloseHandle(job)
		return
	}
	_ = windows.AssignProcessToJobObject(job, windows.CurrentProcess())
	// The handle is intentionally kept open for the lifetime of the process.
}

func setConsoleTitle(title string) {
	p, err := windows.UTF16PtrFromString(title)
	if err != nil {
		return
	}
	proc := windows.NewLazySystemDLL("kernel32.dll").NewProc("SetConsoleTitleW")
	proc.Call(uintptr(unsafe.Pointer(p)))
}
