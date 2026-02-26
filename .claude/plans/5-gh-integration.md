# Plan: GitHub Token & User Support (`--gh-token`)

## Context

AI agents running in dev containers need authenticated git and `gh` CLI access to clone repos, push changes, and use the GitHub API. This adds `--gh-token` to `myringa launch` to handle the full lifecycle: bake `gh` into the image (both distros), interactively collect a PAT + git identity at launch time, inject `GH_TOKEN` into the container's incus environment, and configure git with the credential helper and user identity.

The token is collected via silent terminal prompt (never touches host shell env or history), stored as `environment.GH_TOKEN` in the container's incus config (accessible only to the Incus admin), and used by `gh auth git-credential` as the git credential helper.

---

## Files to Modify

| File | Change |
|---|---|
| `internal/images/embed/packages-alpine.txt` | Add `github-cli` |
| `internal/images/build.go` | Add `installGHCLI()` step after `installPackages`, call for both distros |
| `infra/images/build.sh` | Mirror: `gh` install for Alpine (apk) + Ubuntu (APT repo) after packages step |
| `internal/provision/provision.go` | Add `GHToken`, `GHUserName`, `GHUserEmail` to `LaunchOpts`; new `configureGitHub()`; update `Launch()` + `DryRun()` |
| `main.go` | Add `--gh-token` flag, `promptGHCredentials()`, update `launchUsage` + `isBoolFlag()` |
| `internal/provision/provision_test.go` | Tests for new fields, configureGitHub mock assertions, DryRun output, validation |
| `cmd_test.go` | Test `--gh-token` flag parsing |

---

## Step 1: Bake `gh` CLI into the Image

### `internal/images/embed/packages-alpine.txt`
Add `github-cli` to the package list. The `github-cli` package is available in Alpine 3.21 community repo; the linuxcontainers upstream image has community enabled by default.

### `internal/images/build.go`
Add a new `installGHCLI()` step in `Build()` between step 2 (installPackages) and step 3 (install mise):

```go
// Step 2.5: Install gh CLI.
fmt.Fprintf(out, "Installing gh CLI...\n")
if err := installGHCLI(ctx, c, builder, opts.Distro, out); err != nil {
    return fmt.Errorf("installing gh CLI: %w", err)
}
```

New `installGHCLI()` function:
- **Alpine**: `github-cli` is added to `packages-alpine.txt`, so it's installed in step 2. The function is a no-op for Alpine.
- **Ubuntu**: Add GitHub CLI APT repo + install `gh`, mirroring the Docker APT repo pattern in `installDockerPackages()`:
  ```go
  // Ubuntu only — Alpine handled via packages-alpine.txt
  func installGHCLI(ctx context.Context, c BuildClient, builder, distro string, out io.Writer) error {
      if distro != "ubuntu" {
          return nil
      }
      for _, cmd := range [][]string{
          {"sh", "-c", "install -m 0755 -d /etc/apt/keyrings"},
          {"sh", "-c", "curl -fsSL https://cli.github.com/packages/githubcli-archive-keyring.gpg | dd of=/etc/apt/keyrings/githubcli-archive-keyring.gpg"},
          {"sh", "-c", "chmod go+r /etc/apt/keyrings/githubcli-archive-keyring.gpg"},
          {"sh", "-c", `echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/githubcli-archive-keyring.gpg] https://cli.github.com/packages stable main" > /etc/apt/sources.list.d/github-cli.list`},
          {"apt-get", "update", "-q"},
          {"apt-get", "install", "-y", "-q", "gh"},
      } {
          if err := c.ExecStream(ctx, builder, cmd, out, out); err != nil {
              return fmt.Errorf("installing gh (ubuntu): %w", err)
          }
      }
      return nil
  }
  ```

### `infra/images/build.sh`
Add step 2.5 after the packages step, before mise:

```bash
# ── Step 2.5: Install gh CLI ──────────────────────────────────────────────────

echo "Installing gh CLI..."
case "$DISTRO" in
  alpine)
    # github-cli already installed via packages-alpine.txt (community repo)
    ;;
  ubuntu)
    incus exec "${BUILDER_NAME}" -- sh -c \
      'install -m 0755 -d /etc/apt/keyrings && \
       curl -fsSL https://cli.github.com/packages/githubcli-archive-keyring.gpg | dd of=/etc/apt/keyrings/githubcli-archive-keyring.gpg && \
       chmod go+r /etc/apt/keyrings/githubcli-archive-keyring.gpg && \
       echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/githubcli-archive-keyring.gpg] \
         https://cli.github.com/packages stable main" > /etc/apt/sources.list.d/github-cli.list && \
       apt-get update -q && \
       apt-get install -y -q gh'
    ;;
esac
```

---

## Step 2: Extend `LaunchOpts` and Provisioning

### `internal/provision/provision.go`

**Add fields to `LaunchOpts`:**
```go
// GitHub auth (optional). When GHToken is non-empty, configures GH_TOKEN
// in the container environment and sets git credential helper + identity.
GHToken     string // fine-grained PAT; stored as environment.GH_TOKEN in incus config
GHUserName  string // git user.name (required if GHToken is set)
GHUserEmail string // git user.email (required if GHToken is set)
```

**Update `Validate()`** — add after existing proxy check:
```go
if opts.GHToken != "" {
    if opts.GHUserName == "" {
        return fmt.Errorf("GHUserName is required when GHToken is set")
    }
    if opts.GHUserEmail == "" {
        return fmt.Errorf("GHUserEmail is required when GHToken is set")
    }
}
```

**Update `Launch()`** — after `StartInstance()`:
```go
if opts.GHToken != "" {
    if err := configureGitHub(ctx, c, opts); err != nil {
        return fmt.Errorf("configuring GitHub auth: %w", err)
    }
}
```

**New `configureGitHub()` function:**
```go
// configureGitHub injects GH_TOKEN into the container environment and
// configures the user's git identity and credential helper.
// GH_TOKEN is stored persistently in the incus instance config so that
// gh auth git-credential can authenticate future git operations.
func configureGitHub(ctx context.Context, c Client, opts LaunchOpts) error {
    // 1. Set GH_TOKEN in container environment (all future processes inherit it).
    if err := c.UpdateInstanceConfig(ctx, opts.Name, map[string]string{
        "environment.GH_TOKEN": opts.GHToken,
    }); err != nil {
        return fmt.Errorf("setting GH_TOKEN: %w", err)
    }

    gitcfg := fmt.Sprintf("/home/%s/.gitconfig", opts.Username)

    // 2. Configure git credential helper and identity (as root, writing to user's file).
    cmds := [][]string{
        {"git", "config", "--file", gitcfg, "credential.helper", "!gh auth git-credential"},
        {"git", "config", "--file", gitcfg, "user.name", opts.GHUserName},
        {"git", "config", "--file", gitcfg, "user.email", opts.GHUserEmail},
        // Fix ownership — exec runs as root.
        {"chown", fmt.Sprintf("%d:%d", opts.UID, opts.GID), gitcfg},
    }
    for _, cmd := range cmds {
        if _, err := c.ExecInstance(ctx, opts.Name, cmd); err != nil {
            return fmt.Errorf("running %q: %w", cmd[0], err)
        }
    }
    return nil
}
```

**Update `DryRun()`** — add before the return:
```go
if opts.GHToken != "" {
    fmt.Fprintf(&b, "  GitHub:    GH_TOKEN set; git identity %s <%s>\n", opts.GHUserName, opts.GHUserEmail)
} else {
    fmt.Fprintf(&b, "  GitHub:    not configured (use --gh-token)\n")
}
```

---

## Step 3: CLI Flag, Prompts, and Summary

### `main.go`

**Add `--gh-token` bool flag** in `parseLaunchFlags()`:
```go
ghToken := fs.Bool("gh-token", false, "Configure GitHub CLI + git auth (prompts for PAT and git identity)")
```
`--gh-token` takes no command-line value; credentials are collected interactively after parsing.

After flag parsing, set on `opts` and return the flag value so `runLaunch()` knows to prompt:
```go
// Return ghToken bool alongside opts, OR handle prompting inside parseLaunchFlags.
// Cleanest: return opts with UseGHToken bool field, then prompt in runLaunch().
```
Add `UseGHToken bool` to `LaunchOpts` (provision package) or handle locally in main — prefer keeping it local to `main.go` since it's a UX concern, not a provisioning concern. Use a local `useGHToken` bool returned alongside `opts`.

**Update `isBoolFlag()`:**
```go
"gh-token": true,
```

**Update `launchUsage`:**
```
  --gh-token            Configure GitHub CLI + git auth (prompts for PAT and git identity)
```

**`promptGHCredentials()` in `main.go`:**
```go
type ghCreds struct{ token, name, email string }

func promptGHCredentials() (ghCreds, error) {
    fmt.Println("GitHub fine-grained PAT:")
    fmt.Println("  https://github.com/settings/tokens?type=beta")
    fmt.Print("PAT: ")
    raw, err := term.ReadPassword(int(os.Stdin.Fd()))
    fmt.Println()
    if err != nil {
        return ghCreds{}, fmt.Errorf("reading token: %w", err)
    }
    token := strings.TrimSpace(string(raw))
    if token == "" {
        return ghCreds{}, fmt.Errorf("token must not be empty")
    }

    defaultName  := gitConfigValue("user.name")
    defaultEmail := gitConfigValue("user.email")
    name  := promptLine(fmt.Sprintf("git user.name [%s]: ", defaultName), defaultName)
    email := promptLine(fmt.Sprintf("git user.email [%s]: ", defaultEmail), defaultEmail)

    if name == "" {
        return ghCreds{}, fmt.Errorf("git user.name must not be empty")
    }
    if email == "" {
        return ghCreds{}, fmt.Errorf("git user.email must not be empty")
    }
    return ghCreds{token: token, name: name, email: email}, nil
}

// gitConfigValue reads a key from the host's global git config. Returns "" on any error.
func gitConfigValue(key string) string {
    out, err := exec.Command("git", "config", "--global", key).Output()
    if err != nil {
        return ""
    }
    return strings.TrimSpace(string(out))
}

// promptLine reads a trimmed line from stdin, returning defaultVal on empty input.
func promptLine(prompt, defaultVal string) string {
    fmt.Print(prompt)
    line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
    if v := strings.TrimSpace(line); v != "" {
        return v
    }
    return defaultVal
}
```

**New imports in `main.go`:** `bufio`, `os/exec`, `golang.org/x/term`.

**Final summary in `runLaunch()`:**
```go
fmt.Printf("Container %q is ready.\n", opts.Name)
if opts.GHToken != "" {
    fmt.Printf("  GitHub: GH_TOKEN set — git identity %s <%s>\n", opts.GHUserName, opts.GHUserEmail)
}
```

---

## Step 4: Tests

### `internal/provision/provision_test.go`
- Validation: `GHToken` non-empty + empty `GHUserName` → error; same for `GHUserEmail`.
- DryRun with `GHToken`: output contains git identity line.
- DryRun without `GHToken`: output contains "not configured" line.
- Launch with `GHToken`: mock records `UpdateInstanceConfig` with `environment.GH_TOKEN`; mock records `ExecInstance` calls for `credential.helper`, `user.name`, `user.email`, `chown`.
- Launch without `GHToken`: none of the above exec calls recorded.
- Error propagation: `UpdateInstanceConfig` failure in `configureGitHub` → `Launch` returns wrapped error.

### `cmd_test.go`
- `--gh-token` parsed as a bool flag (no value consumed).
- `isBoolFlag("gh-token")` returns true.

---

## Step 5: `go.mod`

`golang.org/x/term` is already an indirect dep at v0.39.0. Promote to direct by adding it to the top-level `require` block and removing the `// indirect` annotation (or let `go mod tidy` handle it after adding the import).

---

## Verification

1. **Image has gh**: After rebuilding, `incus exec <builder> -- gh --version` succeeds.
2. **Dry-run**: `myringa launch mydev --gh-token --dry-run` → output shows GitHub identity line.
3. **Interactive launch**: `myringa launch mydev --gh-token` → prompts for PAT (silent), name, email → success summary shows auth line.
4. **Inside container**:
   - `gh auth status` → authenticated (reads `GH_TOKEN` from container env).
   - `git config --global credential.helper` → `!gh auth git-credential`.
   - `git config --global user.name` → expected name.
5. **Host shell clean**: `echo $GH_TOKEN` on host shows nothing — token never left Go memory.
