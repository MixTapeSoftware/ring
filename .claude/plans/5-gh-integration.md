# Plan: GitHub Token & SSH Key Support (`--gh-token`)

## Context

AI agents running in dev containers need authenticated git and `gh` CLI access to clone repos, push changes, and use the GitHub API. This adds `--gh-token` to `ring launch` to handle the full lifecycle: bake `gh` into the image, interactively collect a PAT + git identity at launch time, inject `GH_TOKEN` into the container's Incus environment, and configure git with the credential helper and user identity.

---

## Files to Modify

| File | Change |
|---|---|
| `internal/images/embed/packages-alpine.txt` | Add `github-cli` |
| `internal/images/build.go` | Add `installGHCLI()` step for Ubuntu (Alpine gets it from packages); fix step numbering |
| `internal/provision/provision.go` | Add `GHToken`, `GHUserName`, `GHUserEmail` to `LaunchOpts`; new `configureGitHub()`; update `Launch()` + `DryRun()` + `Validate()` |
| `main.go` | Add `--gh-token` bool flag, `promptGHCredentials()`, update `launchUsage` + `isBoolFlag()` |
| `internal/provision/provision_test.go` | Tests for new fields, configureGitHub mock assertions, DryRun output, validation |
| `cmd_test.go` | Test `--gh-token` flag parsing |

---

## Step 1: Bake `gh` CLI into the Image

### `internal/images/embed/packages-alpine.txt`
Add `github-cli` to the package list. Available in Alpine 3.23 community repo (already configured by `installPackages`).

### `internal/images/build.go`
Add `installGHCLI()` as a new build step between step 2 (packages) and step 3 (mise). Current step numbering is 1-7 but has a duplicate "Step 6" bug — fix all step comments while we're here.

New step order after change:
1. Launch builder
2. Install base packages
3. **Install gh CLI (NEW)**
4. Install mise
5. Configure /etc/skel
6. Dev tools
7. Install Claude Code
8. Stop & publish

```go
// Step 3: Install gh CLI (Ubuntu only — Alpine gets it via packages-alpine.txt).
fmt.Fprintf(out, "Installing gh CLI...\n")
if err := installGHCLI(ctx, c, builder, opts.Distro, out); err != nil {
    return fmt.Errorf("installing gh CLI: %w", err)
}
```

New function — mirrors the `installDockerPackages()` pattern for Ubuntu APT repo setup:
```go
func installGHCLI(ctx context.Context, c BuildClient, builder, distro string, out io.Writer) error {
    if distro != "ubuntu" {
        return nil // Alpine: github-cli installed via packages-alpine.txt
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

---

## Step 2: Extend `LaunchOpts` and Provisioning

### `internal/provision/provision.go`

**Add fields to `LaunchOpts`** (after `DryRun`):
```go
GHToken     string // fine-grained PAT; stored as environment.GH_TOKEN in incus config
GHUserName  string // git user.name (required if GHToken is set)
GHUserEmail string // git user.email (required if GHToken is set)
```

**Update `Validate()`** — add after proxy check:
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

**Update `Launch()`** — after `provisionUser()`, before return:
```go
if opts.GHToken != "" {
    if err := configureGitHub(ctx, c, opts); err != nil {
        return fmt.Errorf("configuring GitHub auth: %w", err)
    }
}
```

**New `configureGitHub()`** — follows same `ExecInstance` + `UpdateInstanceConfig` patterns already used for proxy:
```go
func configureGitHub(ctx context.Context, c Client, opts LaunchOpts) error {
    // Set GH_TOKEN in container environment (persists across restarts).
    if err := c.UpdateInstanceConfig(ctx, opts.Name, map[string]string{
        "environment.GH_TOKEN": opts.GHToken,
    }); err != nil {
        return fmt.Errorf("setting GH_TOKEN: %w", err)
    }

    gitcfg := fmt.Sprintf("/home/%s/.gitconfig", opts.Username)
    cmds := [][]string{
        {"git", "config", "--file", gitcfg, "credential.helper", "!gh auth git-credential"},
        {"git", "config", "--file", gitcfg, "user.name", opts.GHUserName},
        {"git", "config", "--file", gitcfg, "user.email", opts.GHUserEmail},
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

**Update `DryRun()`** — add before return:
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

**After flag parsing**, if `*ghToken` is true, call `promptGHCredentials()` to collect token + identity interactively, then set on opts:
```go
if *ghToken {
    creds, err := promptGHCredentials()
    if err != nil {
        return provision.LaunchOpts{}, err
    }
    opts.GHToken = creds.token
    opts.GHUserName = creds.name
    opts.GHUserEmail = creds.email
}
```

**Update `isBoolFlag()`** — add `"gh-token": true` to the map.

**Update `launchUsage`:**
```
  --gh-token            Configure GitHub CLI + git auth (prompts for PAT and git identity)
```

**New `promptGHCredentials()`** — uses `golang.org/x/term` for silent PAT input:
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

    defaultName := gitConfigValue("user.name")
    defaultEmail := gitConfigValue("user.email")
    name := promptLine(fmt.Sprintf("git user.name [%s]: ", defaultName), defaultName)
    email := promptLine(fmt.Sprintf("git user.email [%s]: ", defaultEmail), defaultEmail)

    if name == "" {
        return ghCreds{}, fmt.Errorf("git user.name must not be empty")
    }
    if email == "" {
        return ghCreds{}, fmt.Errorf("git user.email must not be empty")
    }
    return ghCreds{token: token, name: name, email: email}, nil
}
```

Helper functions `gitConfigValue()` and `promptLine()` — small utilities for reading host git config and stdin lines.

**New imports:** `bufio`, `os/exec`, `golang.org/x/term` (already indirect dep at v0.39.0, will become direct).

**Update launch summary** in `runLaunch()`:
```go
if opts.GHToken != "" {
    fmt.Printf("  GitHub: GH_TOKEN set — git identity %s <%s>\n", opts.GHUserName, opts.GHUserEmail)
}
```

---

## Step 4: Tests

### `internal/provision/provision_test.go`
Following existing mock patterns (`newMockClient()`, assertion on `mc.configs`, `mc.execCmds`, `mc.writtenFiles`):
- Validation: `GHToken` non-empty + empty `GHUserName` → error; same for `GHUserEmail`
- DryRun with `GHToken`: output contains git identity line
- DryRun without `GHToken`: output contains "not configured" line
- Launch with `GHToken`: mock records `UpdateInstanceConfig` with `environment.GH_TOKEN`; records `ExecInstance` calls for credential.helper, user.name, user.email, chown
- Launch without `GHToken`: none of the above calls recorded
- Error propagation: `UpdateInstanceConfig` failure → `Launch` returns wrapped error

### `cmd_test.go`
- `--gh-token` parsed as bool flag (no value consumed)
- `isBoolFlag("gh-token")` returns true

---

## Step 5: `go.mod`

`golang.org/x/term` is already indirect at v0.39.0. Run `go mod tidy` after adding the import to promote it to direct.

---

## Verification

1. `go build ./...` — compiles clean
2. `go test ./...` — all tests pass
3. **Image has gh**: After `ring images build alpine`, verify `github-cli` is installed
4. **Dry-run**: `ring launch mydev --gh-token --dry-run` → shows GitHub identity line
5. **Interactive launch**: `ring launch mydev --gh-token` → prompts for PAT (silent), name, email → success summary shows auth line
6. **Inside container**:
   - `gh auth status` → authenticated via GH_TOKEN
   - `git config --global credential.helper` → `!gh auth git-credential`
   - `git config --global user.name` → expected name
7. **Host shell clean**: `echo $GH_TOKEN` on host shows nothing
