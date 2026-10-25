package withdrawals

import (
	"context"
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/xssnick/tonutils-go/address"
	"github.com/xssnick/tonutils-go/liteclient"
	"github.com/xssnick/tonutils-go/tlb"
	"github.com/xssnick/tonutils-go/ton"
	"github.com/xssnick/tonutils-go/ton/wallet"
)

// TonSender pays withdrawals from the project's payout wallet through TON liteservers.
// The wallet's seed phrase comes only from the environment (PAYOUT_WALLET_SEED) and is never logged or stored.
//
// SAFETY: this code moves real money. Run it first on testnet (TON_TESTNET=true) with a few small withdrawals,
// and keep AUTO_PAYOUT=false until that works.
type TonSender struct {
	api  ton.APIClientWrapped
	w    *wallet.Wallet
	addr string
}

// NewTonSender connects to the network and opens the wallet. version is "v4r2" (default) or "v5r1".
func NewTonSender(ctx context.Context, seed, version string, testnet bool) (*TonSender, error) {
	words := strings.Fields(seed)
	if len(words) != 24 {
		return nil, fmt.Errorf("payout wallet seed must have 24 words")
	}
	pool := liteclient.NewConnectionPool()
	cfg := "https://ton.org/global.config.json"
	if testnet {
		cfg = "https://ton.org/testnet-global.config.json"
	}
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := pool.AddConnectionsFromConfigUrl(cctx, cfg); err != nil {
		return nil, fmt.Errorf("connect to TON: %w", err)
	}
	api := ton.NewAPIClient(pool, ton.ProofCheckPolicyFast).WithRetry()
	ver := wallet.V4R2
	if strings.EqualFold(version, "v5r1") {
		ver = wallet.V5R1Final
	}
	w, err := wallet.FromSeed(api, words, ver)
	if err != nil {
		return nil, fmt.Errorf("open payout wallet: %w", err)
	}
	return &TonSender{api: api, w: w, addr: w.WalletAddress().String()}, nil
}

func (t *TonSender) Address() string { return t.addr }

func (t *TonSender) Send(ctx context.Context, payoutID, to string, amountNano int64) (string, error) {
	dst, err := address.ParseAddr(to)
	if err != nil {
		return "", fmt.Errorf("%w: bad address: %v", ErrNotSent, err)
	}
	block, err := t.api.CurrentMasterchainInfo(ctx)
	if err != nil {
		return "", fmt.Errorf("%w: network not reachable: %v", ErrNotSent, err)
	}
	bal, err := t.w.GetBalance(ctx, block)
	if err != nil {
		return "", fmt.Errorf("%w: cannot read wallet balance: %v", ErrNotSent, err)
	}
	need := new(big.Int).Add(big.NewInt(amountNano), big.NewInt(50_000_000)) // 0.05 TON for fees
	if bal.Nano().Cmp(need) < 0 {
		return "", fmt.Errorf("%w: the payout wallet balance is too low", ErrNotSent)
	}
	msg, err := t.w.BuildTransfer(dst, tlb.FromNanoTONU(uint64(amountNano)), false, payoutID)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrNotSent, err)
	}
	sctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	tx, _, err := t.w.SendWaitTransaction(sctx, msg)
	if err != nil {
		return "", err // may or may not have been sent: the caller treats it as unclear
	}
	return hex.EncodeToString(tx.Hash), nil
}
