package withdrawals

import (
	"testing"

	"github.com/xssnick/tonutils-go/ton/wallet"
)

// The payout wallet must open for both supported versions (a W5 wallet needs its network named), and the same words must give
// the same address every time, a different one on the test network, and a different one for V4R2.
func TestOpenWalletVersions(t *testing.T) {
	words := wallet.NewSeed()
	addr := func(version string, testnet bool) string {
		w, err := openWallet(nil, words, version, testnet)
		if err != nil {
			t.Fatalf("%s testnet=%v: %v", version, testnet, err)
		}
		return w.WalletAddress().String()
	}
	v5, v5again, v5test, v4 := addr("v5r1", false), addr("v5r1", false), addr("v5r1", true), addr("v4r2", false)
	if v5 != v5again {
		t.Fatal("the same words must give the same address")
	}
	if v5 == v5test {
		t.Fatal("a W5 wallet on the test network has another address")
	}
	if v5 == v4 {
		t.Fatal("W5 and V4R2 are different wallets")
	}
	if addr("", false) != v4 {
		t.Fatal("the default version is V4R2")
	}
}
