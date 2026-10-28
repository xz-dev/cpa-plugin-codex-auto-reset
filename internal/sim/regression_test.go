package sim

import (
	"testing"
	"time"
)

func TestDelta_RedeemedRowAfterExpiryStillConfirmsConsumption(t *testing.T) {
	E := time.Date(2026, 7, 19, 18, 0, 0, 0, time.UTC)
	cfg := Config{RefreshInterval: time.Hour, TriggerLeadTime: 5 * time.Minute, PostResetDelay: 10 * time.Minute}
	c := newClient("c1", E, 20)
	out := Run(cfg, c, E.Add(-3*time.Hour), 24*time.Hour)
	if out.GaveUp {
		t.Fatalf("retained redeemed row proves consumption even when verification occurs after its expiry (production accepts this), got %s", out.GiveUpReason)
	}
}
