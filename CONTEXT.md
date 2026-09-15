# Rig

Rig bootstraps and manages developer-machine tooling across supported operating systems.

## Language

**Package manager**:
An external tool that Rig can bootstrap. The initial package managers are Nix and Homebrew.
_Avoid_: Dependency manager, installer

**Install target**:
A supported package manager explicitly selected for bootstrapping, or selected by Rig's default ordered set.
_Avoid_: Package, formula

**Installed**:
The state in which an install target's executable is discoverable and its version check succeeds.
_Avoid_: Present, available

**Broken installation**:
The state in which an install target's executable is discoverable but its version check fails. Rig does not treat this state as safe to overwrite.
_Avoid_: Installed, absent

**Partial installation**:
Recognizable state in an install target's canonical location without a working executable. Rig treats this state as unsafe to overwrite or remove automatically.
_Avoid_: Absent, broken installation

**Supported platform**:
An operating-system and architecture combination Rig promises to handle: Apple Silicon macOS 15 or newer, or x86_64/aarch64 Ubuntu 24.04 or newer.
_Avoid_: Best-effort platform

**Nix baseline**:
The system-wide Nix configuration established only when Rig installs Nix. It enables the `nix-command` and `flakes` experimental features while preserving other enabled experimental features.
_Avoid_: Nix configuration management

**Incomplete bootstrap**:
An installed package manager for which Rig's one-time bootstrap additions did not complete. It remains installed, but requires manual remediation.
_Avoid_: Broken installation, partial installation

**Bootstrap**:
Ensure an install target is installed, skipping it when it is already installed. A new Nix installation also receives the Nix baseline.
_Avoid_: Configure, provision
