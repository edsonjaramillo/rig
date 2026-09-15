package cmd

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
)

const (
	installUse             = "install [nix|homebrew]"
	nixCanonicalExecutable = "/nix/var/nix/profiles/default/bin/nix"
	nixStateRoot           = "/nix"
)

var installTargets = map[InstallTarget]installTargetSpec{
	Nix: {
		executable:           "nix",
		canonicalExecutables: []string{nixCanonicalExecutable},
		canonicalStateRoots:  []string{nixStateRoot},
	},
	Homebrew: {
		executable: "brew",
		canonicalExecutables: []string{
			macOSHomebrewExecutable,
			linuxHomebrewExecutable,
			"/usr/local/bin/brew",
		},
		canonicalStateRoots: []string{
			"/opt/homebrew",
			"/home/linuxbrew/.linuxbrew",
			"/usr/local/Homebrew",
		},
	},
}

// InstallTarget identifies a package manager Rig can bootstrap.
type InstallTarget string

// Supported install targets.
const (
	Nix      InstallTarget = "nix"
	Homebrew InstallTarget = "homebrew"
)

type installTargetSpec struct {
	executable           string
	canonicalExecutables []string
	canonicalStateRoots  []string
}

type usageError struct {
	message string
}

func (e *usageError) Error() string {
	return e.message
}

func newInstallCommand(host Host, streams Streams) *cobra.Command {
	command := &cobra.Command{
		Use:           installUse,
		Short:         "Bootstrap Nix and Homebrew",
		SilenceErrors: true,
		SilenceUsage:  true,
		Args:          validateInstallArgs,
		RunE: func(command *cobra.Command, args []string) error {
			for _, target := range selectedInstallTargets(args) {
				if err := ensureInstalled(command.Context(), host, streams, target); err != nil {
					return err
				}
			}
			return nil
		},
	}
	command.SetFlagErrorFunc(func(_ *cobra.Command, err error) error {
		return &usageError{message: err.Error()}
	})
	return command
}

func validateInstallHelpArgs(commandLine []string) error {
	if len(commandLine) == 0 || commandLine[0] != "install" {
		return nil
	}

	helpRequested := false
	positional := make([]string, 0, len(commandLine)-1)
	for _, argument := range commandLine[1:] {
		switch argument {
		case "-h", "--help":
			helpRequested = true
		default:
			positional = append(positional, argument)
		}
	}
	if !helpRequested {
		return nil
	}
	return validateInstallArgs(nil, positional)
}

func validateInstallArgs(_ *cobra.Command, args []string) error {
	if len(args) > 1 {
		return &usageError{message: "install accepts at most one target"}
	}
	if len(args) == 1 && args[0] != string(Nix) && args[0] != string(Homebrew) {
		return &usageError{message: fmt.Sprintf("unknown install target %q", args[0])}
	}
	return nil
}

func selectedInstallTargets(args []string) []InstallTarget {
	if len(args) == 0 {
		return []InstallTarget{Nix, Homebrew}
	}
	return []InstallTarget{InstallTarget(args[0])}
}

func validateSupportedPlatform(platform Platform, target InstallTarget) error {
	majorText := strings.SplitN(strings.TrimSpace(platform.Version), ".", 2)[0]
	major, err := strconv.Atoi(majorText)
	if err != nil {
		return fmt.Errorf("unsupported platform for %s: %s %s %s", target, platform.OS, platform.Architecture, platform.Version)
	}

	supported := platform.OS == "darwin" && platform.Architecture == "arm64" && major >= 15 ||
		platform.OS == "linux" && strings.EqualFold(platform.Distribution, "ubuntu") && major >= 24 &&
			(platform.Architecture == "amd64" || platform.Architecture == "arm64" ||
				platform.Architecture == "x86_64" || platform.Architecture == "aarch64")
	if !supported {
		return fmt.Errorf("unsupported platform for %s: %s %s %s", target, platform.OS, platform.Architecture, platform.Version)
	}
	return nil
}

func missingPrerequisites(host Host, target InstallTarget, executables []string) ([]string, error) {
	missing := make([]string, 0)
	for _, executable := range executables {
		_, err := host.LookPath(executable)
		switch {
		case err == nil:
		case errors.Is(err, ErrNotFound):
			missing = append(missing, executable)
		default:
			return nil, fmt.Errorf("inspect %s prerequisite %s: %w", target, executable, err)
		}
	}
	return missing, nil
}

func ensureInstalled(ctx context.Context, host Host, streams Streams, target InstallTarget) error {
	spec := installTargets[target]

	path, err := host.LookPath(spec.executable)
	switch {
	case err == nil:
		return checkVersion(ctx, host, target, path)
	case !errors.Is(err, ErrNotFound):
		return fmt.Errorf("inspect PATH for %s: %w", target, err)
	}

	for _, canonicalExecutable := range spec.canonicalExecutables {
		info, err := host.InspectPath(canonicalExecutable)
		if err != nil {
			return fmt.Errorf("inspect canonical %s executable: %w", target, err)
		}
		if info.Executable {
			return checkVersion(ctx, host, target, canonicalExecutable)
		}
		if info.Exists {
			return fmt.Errorf("%s has a partial installation at %s", target, canonicalExecutable)
		}
	}

	for _, stateRoot := range spec.canonicalStateRoots {
		info, err := host.InspectPath(stateRoot)
		if err != nil {
			return fmt.Errorf("inspect canonical %s state: %w", target, err)
		}
		if info.Exists {
			return fmt.Errorf("%s has a partial installation at %s", target, stateRoot)
		}
	}

	switch target {
	case Nix:
		return bootstrapNix(ctx, host, streams)
	case Homebrew:
		return bootstrapHomebrew(ctx, host, streams)
	default:
		return fmt.Errorf("bootstrap %s: unsupported install target", target)
	}
}

func checkVersion(ctx context.Context, host Host, target InstallTarget, executable string) error {
	err := host.Run(ctx, Command{Path: executable, Args: []string{"--version"}})
	if err != nil {
		return fmt.Errorf("%s has a broken installation at %s: version check failed: %w", target, executable, err)
	}
	return nil
}
