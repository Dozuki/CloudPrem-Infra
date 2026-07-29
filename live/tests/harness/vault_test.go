package harness

import (
	"context"
	"testing"
	"time"
)

// The renewer only helps if it fires comfortably inside the token's life. The Vault
// AWS-auth role issues a 1h token (measured, not assumed: creation_ttl=3600,
// explicit_max_ttl=0, renewable). Renewing resets the lease to a fresh hour, so the
// interval just has to leave room for a renewal to FAIL and still be retried before the
// token dies - otherwise a single transient Vault blip puts the run back exactly where
// cycle 45 was, ~80 minutes in with a dead token and an apply in flight.
func TestVaultRenewIntervalLeavesRoomForAMissedRenewal(t *testing.T) {
	const tokenTTL = time.Hour

	if vaultRenewInterval >= tokenTTL {
		t.Fatalf("renew interval %s is not inside the %s token TTL - the token dies before it is ever renewed",
			vaultRenewInterval, tokenTTL)
	}
	if 2*vaultRenewInterval >= tokenTTL {
		t.Fatalf("renew interval %s leaves no margin: one failed renewal and the %s token expires before the next attempt",
			vaultRenewInterval, tokenTTL)
	}
}

// The retry must fire on an expired token and NOT on a real authorization failure.
// Retrying a policy problem just turns an instant, readable error into three more
// re-logins and a confusing timeout.
func TestVaultTokenExpiredREMatchesOnlyTheProvidersOwnTokenLookup(t *testing.T) {
	// Verbatim from cycle 45's rebuild apply.
	expired := `Error: failed to lookup token, err=Error making API request.

URL: GET http://127.0.0.1:8200/v1/auth/token/lookup-self
Code: 403. Errors:

* permission denied`

	if !vaultTokenExpiredRE.MatchString(expired) {
		t.Fatal("expired-token error not matched; the retry would never fire and a run dies at the 1h mark")
	}

	// A live token that simply is not allowed to do this. Must fail immediately.
	for name, out := range map[string]string{
		"policy denial on a kv write": `Error: error writing to Vault: Error making API request.

URL: PUT http://127.0.0.1:8200/v1/secret/data/dozuki/smoke
Code: 403. Errors:

* 1 error occurred:
	* permission denied`,
		"bare permission denied": "Error: permission denied",
		"sealed vault":           "Error: Vault is sealed",
	} {
		if vaultTokenExpiredRE.MatchString(out) {
			t.Errorf("%s: matched the expired-token retry, but re-logging in cannot fix it", name)
		}
	}
}

// No Vault tunnel means there is nothing to renew. This must return rather than spawn a
// goroutine that shells out to a missing binary every 20 minutes for the life of an
// azure or SKIP_VAULT_TUNNEL run.
func TestStartVaultTokenRenewerIsANoopWithoutVaultAddr(t *testing.T) {
	t.Setenv("VAULT_ADDR", "")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		StartVaultTokenRenewer(ctx)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("StartVaultTokenRenewer blocked with no VAULT_ADDR; it must return immediately")
	}
}
