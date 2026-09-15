package cmd

import (
	"context"
	"errors"
	"fmt"

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

	if target == Homebrew {
		return bootstrapHomebrew(ctx, host, streams)
	}
	if err := host.Bootstrap(ctx, target, streams); err != nil {
		return fmt.Errorf("bootstrap %s: %w", target, err)
	}
	return nil
}

func checkVersion(ctx context.Context, host Host, target InstallTarget, executable string) error {
	err := host.Run(ctx, Command{Path: executable, Args: []string{"--version"}})
	if err != nil {
		return fmt.Errorf("%s has a broken installation at %s: version check failed: %w", target, executable, err)
	}
	return nil
}
