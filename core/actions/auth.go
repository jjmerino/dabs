package actions

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/jjmerino/dabs/core/params"
	"github.com/jjmerino/dabs/core/sandbox"
)

// authClaudeDir is the box path where Claude Code keeps its credential. The
// vault is mounted here read-write so the login — and every later token
// refresh — writes straight through to the host and outlives the box.
const authClaudeDir = "/root/.claude"

// Auth logs a harness into a persistent host vault. It boots a throwaway box
// with the vault mounted live at the harness's credential path, runs the
// harness's login flow interactively, and tears the box down — leaving the
// refreshed credential in the vault for future sandboxes to mount.
func (r Real) Auth(p params.Auth) error {
	if p.Provider != "claude" {
		return fmt.Errorf("auth: unknown provider %q (known: claude)", p.Provider)
	}
	drv, err := r.driverFor("") // auth is always a local concern
	if err != nil {
		return err
	}

	vault, err := vaultDir(p.Provider)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(vault, 0o700); err != nil {
		return fmt.Errorf("auth: vault %s: %w", vault, err)
	}

	// The login box's image. Normally dabs builds it from the recipe bundled
	// in the binary. DABS_AUTH_IMAGE names an already-built image to reuse
	// instead — how a no-docker environment (the e2e runner, staged prebuilt
	// images) supplies a box whose `claude` is a fake that only handles login.
	name := "auth-" + p.Provider
	if img := os.Getenv("DABS_AUTH_IMAGE"); img != "" {
		name = img
	} else {
		ctxDir, err := r.stageImage(p.Provider)
		if err != nil {
			return err
		}
		defer os.RemoveAll(ctxDir)
		if err := drv.Build(sandbox.BuildSpec{
			Name:       name,
			Dockerfile: filepath.Join(ctxDir, "Dockerfile"),
			Context:    ctxDir,
		}); err != nil {
			return err
		}
	}

	instance, err := drv.Up(sandbox.Spec{
		Name:    name,
		Workdir: "/work",
		Mounts:  []sandbox.Mount{{Host: vault, Path: authClaudeDir}},
	})
	if err != nil {
		return err
	}
	defer drv.Down(instance)

	// The login runs interactively in the box; the user completes onboarding
	// and exits Claude themselves. The credential writes through the mount to
	// this host file, where a completed flow leaves a non-empty
	// claudeAiOauth.accessToken for future sandboxes to mount.
	credPath := filepath.Join(vault, ".credentials.json")

	fmt.Fprintf(os.Stdout, "\nThe next step must be completed by you. When Claude appears, /login "+
		"and complete the initial setup. Once done, /exit Claude to continue.\n\n")
	if err := drv.Run(instance, []string{"claude"}); err != nil {
		return fmt.Errorf("auth: login: %w", err)
	}

	if credAccessToken(credPath) == "" {
		return fmt.Errorf("auth: login did not produce a credential (not completed?)")
	}
	fmt.Fprintf(os.Stdout, "claude authenticated → %s\n", vault)
	return nil
}

// credAccessToken reads claudeAiOauth.accessToken from a Claude credential
// file, returning "" if the file is missing, unreadable, or unparseable.
func credAccessToken(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var doc struct {
		ClaudeAiOauth struct {
			AccessToken string `json:"accessToken"`
		} `json:"claudeAiOauth"`
	}
	if json.Unmarshal(data, &doc) != nil {
		return ""
	}
	return doc.ClaudeAiOauth.AccessToken
}

// vaultDir is the host directory that holds a provider's credential, mounted
// live into future sandboxes.
func vaultDir(provider string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("auth: home: %w", err)
	}
	return filepath.Join(home, ".dabs", "auth", provider), nil
}

// stageImage materializes the bundled build recipe for a provider into a temp
// directory the driver can build from.
func (r Real) stageImage(provider string) (string, error) {
	sub := "images/" + provider
	dir, err := os.MkdirTemp("", "dabs-auth-"+provider+"-")
	if err != nil {
		return "", fmt.Errorf("auth: stage: %w", err)
	}
	err = fs.WalkDir(r.images, sub, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(sub, p)
		dst := filepath.Join(dir, rel)
		if d.IsDir() {
			return os.MkdirAll(dst, 0o755)
		}
		data, err := fs.ReadFile(r.images, p)
		if err != nil {
			return err
		}
		return os.WriteFile(dst, data, 0o644)
	})
	if err != nil {
		os.RemoveAll(dir)
		return "", fmt.Errorf("auth: stage %s: %w", sub, err)
	}
	return dir, nil
}
