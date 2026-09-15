package cmd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
)

const (
	nixInstallerURL         = "https://nixos.org/nix/install"
	nixInstallerSizeLimit   = 1024 * 1024
	nixInstallerTempPattern = "rig-nix-installer-*"
	nixConfigTempPattern    = "rig-nix-config-*"
	systemdRuntimePath      = "/run/systemd/system"
	nixBaselineConfig       = "extra-experimental-features = nix-command flakes\n"
)

var (
	nixDaemonVerificationArgs = []string{
		"--extra-experimental-features", "nix-command", "store", "ping", "--store", "daemon",
	}
	nixBaselineVerificationArgs = []string{"config", "show", "experimental-features"}
	nixVerificationEnvironment  = []string{"HOME=/var/empty", "XDG_CONFIG_HOME=/var/empty"}
	commonNixPrerequisites      = []string{
		"awk", "bash", "cat", "chmod", "chown", "cp", "cut", "diff", "dirname", "env", "grep", "head",
		"id", "install", "mkdir", "mktemp", "mv", "rm", "sed", "seq", "sleep", "sudo", "tar", "tee",
		"touch", "tr", "uname",
	}
	ubuntuNixPrerequisites = []string{
		"getent", "groupadd", "groups", "ln", "systemctl", "systemd-tmpfiles", "useradd", "usermod", "xz",
	}
	macOSNixPrerequisites = []string{
		"diskutil", "dscl", "dseditgroup", "fdesetup", "launchctl", "plutil", "security", "stat", "xmllint",
	}
)

func bootstrapNix(ctx context.Context, host Host, streams Streams) error {
	platform, err := host.Platform(ctx)
	if err != nil {
		return fmt.Errorf("inspect platform for nix: %w", err)
	}
	if err := validateSupportedPlatform(platform, Nix); err != nil {
		return err
	}

	currentUser, err := host.User(ctx)
	if err != nil {
		return fmt.Errorf("inspect current user for nix: %w", err)
	}
	if currentUser.UID == 0 {
		return errors.New("nix bootstrap must run as a regular user, not root")
	}
	if !host.IsTerminal() {
		return errors.New("nix bootstrap requires an interactive terminal")
	}

	if platform.OS == "linux" {
		if err := validateNixHostCapabilities(ctx, host, platform); err != nil {
			return err
		}
	}
	missing, err := missingNixPrerequisites(host, platform)
	if err != nil {
		return err
	}
	if len(missing) > 0 {
		return fmt.Errorf("nix prerequisites are missing: %s", strings.Join(missing, ", "))
	}
	if platform.OS == "darwin" {
		if err := validateNixHostCapabilities(ctx, host, platform); err != nil {
			return err
		}
	}
	if err := host.ValidateSudo(ctx); err != nil {
		return fmt.Errorf("validate sudo for nix: %w", err)
	}

	if err := installNix(ctx, host, streams); err != nil {
		return err
	}
	if err := verifyNewNix(ctx, host); err != nil {
		return fmt.Errorf("nix incomplete bootstrap; manual remediation required: %w", err)
	}
	return nil
}

func validateNixHostCapabilities(ctx context.Context, host Host, platform Platform) error {
	if platform.OS == "linux" {
		info, err := host.InspectPath(systemdRuntimePath)
		if err != nil {
			return fmt.Errorf("inspect systemd runtime: %w", err)
		}
		if !info.Exists {
			return errors.New("nix bootstrap requires a running systemd environment")
		}
		var state bytes.Buffer
		err = host.Run(ctx, Command{
			Path: "systemctl",
			Args: []string{"is-system-running"},
			Streams: Streams{
				Stdout: &state,
				Stderr: io.Discard,
			},
		})
		if err != nil && strings.TrimSpace(state.String()) != "degraded" {
			return fmt.Errorf("nix bootstrap requires a running systemd environment: %w", err)
		}
		return nil
	}

	if err := host.Run(ctx, Command{Path: "/usr/sbin/diskutil", Args: []string{"info", "/"}}); err != nil {
		return fmt.Errorf("validate macOS disk administration capability: %w", err)
	}
	if err := host.Run(ctx, Command{Path: "/usr/bin/dscl", Args: []string{".", "-read", "/Users/root", "UniqueID"}}); err != nil {
		return fmt.Errorf("validate macOS directory administration capability: %w", err)
	}
	return nil
}

func missingNixPrerequisites(host Host, platform Platform) ([]string, error) {
	prerequisites := slices.Clone(commonNixPrerequisites)
	if platform.OS == "linux" {
		prerequisites = append(prerequisites, ubuntuNixPrerequisites...)
	} else {
		prerequisites = append(prerequisites, macOSNixPrerequisites...)
	}

	missing, err := missingPrerequisites(host, Nix, prerequisites)
	if err != nil {
		return nil, err
	}

	downloaderMissing, err := missingExecutableChoice(host, []string{"curl", "wget"})
	if err != nil {
		return nil, err
	}
	if downloaderMissing {
		missing = append(missing, "curl or wget")
	}
	hashMissing, err := missingExecutableChoice(host, []string{"sha256sum", "shasum", "openssl"})
	if err != nil {
		return nil, err
	}
	if hashMissing {
		missing = append(missing, "sha256sum, shasum, or openssl")
	}
	return missing, nil
}

func missingExecutableChoice(host Host, alternatives []string) (bool, error) {
	for _, executable := range alternatives {
		_, err := host.LookPath(executable)
		switch {
		case err == nil:
			return false, nil
		case errors.Is(err, ErrNotFound):
		default:
			return false, fmt.Errorf("inspect nix prerequisite %s: %w", executable, err)
		}
	}
	return true, nil
}

func installNix(ctx context.Context, host Host, streams Streams) (result error) {
	installer, err := retrieveInstaller(ctx, host, Nix, nixInstallerURL, nixInstallerTempPattern, nixInstallerSizeLimit)
	if err != nil {
		return err
	}
	defer finishTemporaryArtifact(installer, "nix installer", &result)

	configuration, err := createNixConfiguration(host)
	if err != nil {
		return err
	}
	defer finishTemporaryArtifact(configuration, "nix configuration", &result)

	err = host.Run(ctx, Command{
		Path: "/bin/bash",
		Args: []string{
			installer.Name,
			"--daemon",
			"--nix-extra-conf-file", configuration.Name,
		},
		Interactive: true,
		Streams:     streams,
	})
	if errors.Is(err, context.Canceled) {
		return &cancellationError{}
	}
	if err != nil {
		return errors.New("nix installer failed")
	}
	return nil
}

func createNixConfiguration(host Host) (TemporaryArtifact, error) {
	artifact, err := host.CreateTemp(nixConfigTempPattern)
	if err != nil {
		return TemporaryArtifact{}, fmt.Errorf("create temporary nix configuration: %w", err)
	}
	if err := fillTemporaryArtifact(artifact, strings.NewReader(nixBaselineConfig), int64(len(nixBaselineConfig))); err != nil {
		result := fmt.Errorf("prepare temporary nix configuration: %w", err)
		finishTemporaryArtifact(artifact, "nix configuration", &result)
		return TemporaryArtifact{}, result
	}
	return artifact, nil
}

func verifyNewNix(ctx context.Context, host Host) error {
	if err := host.Run(ctx, Command{Path: nixCanonicalExecutable, Args: []string{"--version"}}); err != nil {
		return fmt.Errorf("version check failed: %w", err)
	}
	if err := host.Run(ctx, Command{Path: nixCanonicalExecutable, Args: nixDaemonVerificationArgs}); err != nil {
		return fmt.Errorf("daemon is not ready: %w", err)
	}

	var output bytes.Buffer
	if err := host.Run(ctx, Command{
		Path: nixCanonicalExecutable,
		Args: nixBaselineVerificationArgs,
		Env:  nixVerificationEnvironment,
		Streams: Streams{
			Stdout: &output,
			Stderr: io.Discard,
		},
	}); err != nil {
		return fmt.Errorf("sanitized nix baseline verification failed: %w", err)
	}
	features := strings.Fields(output.String())
	for _, required := range []string{"nix-command", "flakes"} {
		if !slices.Contains(features, required) {
			return fmt.Errorf("sanitized system configuration does not enable %s", required)
		}
	}
	return nil
}
