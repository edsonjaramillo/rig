package cmd

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

const (
	homebrewInstallerURL       = "https://raw.githubusercontent.com/Homebrew/install/HEAD/install.sh"
	homebrewInstallerSizeLimit = 1024 * 1024
	macOSHomebrewExecutable    = "/opt/homebrew/bin/brew"
	linuxHomebrewExecutable    = "/home/linuxbrew/.linuxbrew/bin/brew"
)

var ubuntuHomebrewPrerequisites = []string{
	"git", "curl", "cc", "c++", "make", "ar", "ld", "ps", "file", "tar",
}

func bootstrapHomebrew(ctx context.Context, host Host, streams Streams) error {
	platform, err := host.Platform(ctx)
	if err != nil {
		return fmt.Errorf("inspect platform for homebrew: %w", err)
	}
	canonicalExecutable, err := supportedHomebrewExecutable(platform)
	if err != nil {
		return err
	}

	currentUser, err := host.User(ctx)
	if err != nil {
		return fmt.Errorf("inspect current user for homebrew: %w", err)
	}
	if currentUser.UID == 0 {
		return errors.New("homebrew bootstrap must run as a regular user, not root")
	}
	if !host.IsTerminal() {
		return errors.New("homebrew bootstrap requires an interactive terminal")
	}

	missing, err := missingHomebrewPrerequisites(ctx, host, platform)
	if err != nil {
		return err
	}
	if len(missing) > 0 {
		return fmt.Errorf("homebrew prerequisites are missing: %s", strings.Join(missing, ", "))
	}
	if err := host.ValidateSudo(ctx); err != nil {
		return fmt.Errorf("validate sudo for homebrew: %w", err)
	}

	if err := installHomebrew(ctx, host, streams); err != nil {
		return err
	}
	if err := host.Run(ctx, Command{Path: canonicalExecutable, Args: []string{"--version"}}); err != nil {
		return fmt.Errorf("verify homebrew at %s: %w", canonicalExecutable, err)
	}
	return nil
}

func supportedHomebrewExecutable(platform Platform) (string, error) {
	if err := validateSupportedPlatform(platform, Homebrew); err != nil {
		return "", err
	}
	if platform.OS == "darwin" {
		return macOSHomebrewExecutable, nil
	}
	return linuxHomebrewExecutable, nil
}

func missingHomebrewPrerequisites(ctx context.Context, host Host, platform Platform) ([]string, error) {
	if platform.OS == "darwin" {
		if err := host.Run(ctx, Command{Path: "/usr/bin/xcrun", Args: []string{"clang", "--version"}}); err != nil {
			return []string{"Xcode Command Line Tools with an accepted license"}, nil
		}
		return nil, nil
	}

	return missingPrerequisites(host, Homebrew, ubuntuHomebrewPrerequisites)
}

func installHomebrew(ctx context.Context, host Host, streams Streams) (result error) {
	artifact, err := retrieveInstaller(
		ctx,
		host,
		Homebrew,
		homebrewInstallerURL,
		"rig-homebrew-*",
		homebrewInstallerSizeLimit,
	)
	if err != nil {
		return err
	}
	defer finishTemporaryArtifact(artifact, "homebrew installer", &result)

	if err := host.Run(ctx, Command{
		Path:        "/bin/bash",
		Args:        []string{artifact.Name},
		Interactive: true,
		Streams:     streams,
	}); err != nil {
		if errors.Is(err, context.Canceled) {
			return &cancellationError{}
		}
		return errors.New("homebrew installer failed")
	}
	return nil
}
