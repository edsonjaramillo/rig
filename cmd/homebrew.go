package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strconv"
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
	major, err := platformMajorVersion(platform.Version)
	if err != nil {
		return "", fmt.Errorf("unsupported platform for homebrew: %s %s %s", platform.OS, platform.Architecture, platform.Version)
	}

	switch {
	case platform.OS == "darwin" && platform.Architecture == "arm64" && major >= 15:
		return macOSHomebrewExecutable, nil
	case platform.OS == "linux" && strings.EqualFold(platform.Distribution, "ubuntu") &&
		(platform.Architecture == "amd64" || platform.Architecture == "arm64" ||
			platform.Architecture == "x86_64" || platform.Architecture == "aarch64") && major >= 24:
		return linuxHomebrewExecutable, nil
	default:
		return "", fmt.Errorf("unsupported platform for homebrew: %s %s %s", platform.OS, platform.Architecture, platform.Version)
	}
}

func platformMajorVersion(version string) (int, error) {
	majorText := strings.SplitN(strings.TrimSpace(version), ".", 2)[0]
	return strconv.Atoi(majorText)
}

func missingHomebrewPrerequisites(ctx context.Context, host Host, platform Platform) ([]string, error) {
	if platform.OS == "darwin" {
		if err := host.Run(ctx, Command{Path: "/usr/bin/xcrun", Args: []string{"clang", "--version"}}); err != nil {
			return []string{"Xcode Command Line Tools with an accepted license"}, nil
		}
		return nil, nil
	}

	missing := make([]string, 0)
	for _, executable := range ubuntuHomebrewPrerequisites {
		_, err := host.LookPath(executable)
		switch {
		case err == nil:
		case errors.Is(err, ErrNotFound):
			missing = append(missing, executable)
		default:
			return nil, fmt.Errorf("inspect homebrew prerequisite %s: %w", executable, err)
		}
	}
	return missing, nil
}

func installHomebrew(ctx context.Context, host Host, streams Streams) error {
	response, err := host.Retrieve(ctx, homebrewInstallerURL)
	if err != nil {
		return fmt.Errorf("download homebrew installer: %w", err)
	}
	if response.Body == nil {
		return errors.New("download homebrew installer: empty response body")
	}
	defer func() {
		_ = response.Body.Close()
	}()

	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("download homebrew installer: unexpected HTTP status %d", response.StatusCode)
	}
	finalURL, err := url.Parse(response.FinalURL)
	if err != nil || !strings.EqualFold(finalURL.Scheme, "https") {
		return errors.New("download homebrew installer: redirect did not remain on HTTPS")
	}

	artifact, err := host.CreateTemp("rig-homebrew-*")
	if err != nil {
		return fmt.Errorf("create temporary homebrew installer: %w", err)
	}
	return useHomebrewInstaller(ctx, host, streams, artifact, response.Body)
}

func useHomebrewInstaller(
	ctx context.Context,
	host Host,
	streams Streams,
	artifact TemporaryArtifact,
	body io.Reader,
) (result error) {
	defer func() {
		if artifact.Remove == nil {
			return
		}
		if err := artifact.Remove(); err != nil && result == nil {
			result = fmt.Errorf("remove temporary homebrew installer: %w", err)
		}
	}()

	if artifact.File == nil {
		return errors.New("create temporary homebrew installer: no file returned")
	}
	limited := io.LimitReader(body, homebrewInstallerSizeLimit+1)
	written, err := io.Copy(artifact.File, limited)
	if err != nil {
		_ = artifact.File.Close()
		return fmt.Errorf("write temporary homebrew installer: %w", err)
	}
	if written > homebrewInstallerSizeLimit {
		_ = artifact.File.Close()
		return fmt.Errorf("download homebrew installer: response exceeds %d bytes", homebrewInstallerSizeLimit)
	}
	if err := artifact.File.Close(); err != nil {
		return fmt.Errorf("close temporary homebrew installer: %w", err)
	}

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
