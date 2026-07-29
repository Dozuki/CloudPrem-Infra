package harness

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// The Vault AWS-auth token run.sh logs in with has a 1 hour TTL. Any run longer than
// that loses Vault the moment the next logical apply touches it: every vault data source
// and resource fails with `failed to lookup token ... 403 permission denied` on
// auth/token/lookup-self. A dead PORT-FORWARD looks different (connection refused), so
// the 403 is specifically an expired token.
//
// This is not a rare edge. The recovery scenario cannot avoid it: the source stack, the
// drill, the snapshot and the rebuild run in sequence, so the rebuild's logical layer
// always lands past the hour mark. Cycle 45 died exactly there, ~81 minutes in, having
// already done every expensive thing correctly. The upgrade scenario is exposed too - it
// applies logical twice - it just tended to fail earlier for other reasons.
//
// Renewal rather than re-login on purpose: the token is renewable with no explicit max
// TTL, so `vault token renew` resets the lease WITHOUT changing the token value. That
// matters because VAULT_TOKEN has already been copied into the environment of any child
// process; re-login mints a different token and only helps processes started afterwards.
// Re-login stays as the fallback for when the token is already dead.
const vaultRenewInterval = 20 * time.Minute

// StartVaultTokenRenewer keeps the run's Vault token alive until ctx is cancelled.
// Best-effort by design: no Vault tunnel (azure / SKIP_VAULT_TUNNEL) or no vault CLI
// means there is nothing to renew, and a failed renewal is reported rather than fatal -
// the apply that needs the token will fail loudly on its own if this cannot recover.
//
// Pass a context that outlives teardown; the logical destroy reads Vault too.
func StartVaultTokenRenewer(ctx context.Context) {
	if os.Getenv("VAULT_ADDR") == "" {
		return
	}
	if _, err := exec.LookPath("vault"); err != nil {
		return
	}
	go func() {
		t := time.NewTicker(vaultRenewInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				renewVaultToken()
			}
		}
	}()
}

// renewVaultToken extends the current token's lease, falling back to a full re-login if
// the token is too far gone to renew.
func renewVaultToken() {
	out, err := exec.Command("vault", "token", "renew", "-format=json").CombinedOutput()
	if err == nil {
		fmt.Fprintf(os.Stderr, ">> vault: token lease renewed (next in %s)\n", vaultRenewInterval)
		return
	}
	fmt.Fprintf(os.Stderr, ">> vault: token renew failed (%v: %s) — re-logging in\n",
		err, truncate(strings.TrimSpace(string(out)), 200))
	refreshVaultToken()
}
