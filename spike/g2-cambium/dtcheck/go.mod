// Spike harness for ADR 0002. Not part of the library module.
// Build: point the replace at a checkout of github.com/signalbreak-labs/cambium (commit 884a3fd).
module spike/g2/dtcheck

go 1.26

require github.com/signalbreak-labs/cambium v0.0.0

replace github.com/signalbreak-labs/cambium => ../../../../cambium-checkout
