// Package ton has the TON helpers: address validation, TON Connect comment payloads, nanoTON maths, and a chain reader.
package ton

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Nano is the number of nanoTON in one TON.
const Nano int64 = 1_000_000_000

var rawAddr = regexp.MustCompile(`^(-?\d+):([0-9a-fA-F]{64})$`)

// crc16 is the XMODEM CRC used by TON friendly addresses.
func crc16(data []byte) uint16 {
	var crc uint16
	for _, b := range data {
		crc ^= uint16(b) << 8
		for i := 0; i < 8; i++ {
			if crc&0x8000 != 0 {
				crc = crc<<1 ^ 0x1021
			} else {
				crc <<= 1
			}
		}
	}
	return crc
}

type Address struct {
	Workchain int8
	Hash      [32]byte
	Bounce    bool
	TestOnly  bool
}

var ErrBadAddress = errors.New("ton: invalid address")

// ParseAddress accepts the raw form ("0:abcd...") and the user-friendly forms (base64 or base64url, 48 characters, with CRC).
func ParseAddress(s string) (*Address, error) {
	s = strings.TrimSpace(s)
	if m := rawAddr.FindStringSubmatch(s); m != nil {
		wc, err := strconv.ParseInt(m[1], 10, 8)
		if err != nil || (wc != 0 && wc != -1) {
			return nil, ErrBadAddress
		}
		var a Address
		a.Workchain = int8(wc)
		h, _ := hex.DecodeString(m[2])
		copy(a.Hash[:], h)
		a.Bounce = true
		return &a, nil
	}
	if len(s) != 48 {
		return nil, ErrBadAddress
	}
	var raw []byte
	var err error
	if strings.ContainsAny(s, "-_") {
		raw, err = base64.RawURLEncoding.DecodeString(s)
	} else {
		raw, err = base64.StdEncoding.DecodeString(s)
	}
	if err != nil || len(raw) != 36 {
		return nil, ErrBadAddress
	}
	if crc16(raw[:34]) != binary.BigEndian.Uint16(raw[34:]) {
		return nil, ErrBadAddress
	}
	tag := raw[0]
	test := tag&0x80 != 0
	tag &^= 0x80
	if tag != 0x11 && tag != 0x51 {
		return nil, ErrBadAddress
	}
	wc := int8(raw[1])
	if wc != 0 && wc != -1 {
		return nil, ErrBadAddress
	}
	a := &Address{Workchain: wc, Bounce: tag == 0x11, TestOnly: test}
	copy(a.Hash[:], raw[2:34])
	return a, nil
}

// Raw returns "workchain:hex".
func (a *Address) Raw() string {
	return fmt.Sprintf("%d:%s", a.Workchain, hex.EncodeToString(a.Hash[:]))
}

// Friendly returns the user-friendly base64url form.
func (a *Address) Friendly(bounceable bool) string {
	b := make([]byte, 36)
	b[0] = 0x51
	if bounceable {
		b[0] = 0x11
	}
	if a.TestOnly {
		b[0] |= 0x80
	}
	b[1] = byte(a.Workchain)
	copy(b[2:34], a.Hash[:])
	binary.BigEndian.PutUint16(b[34:], crc16(b[:34]))
	return base64.RawURLEncoding.EncodeToString(b)
}

// ValidAddress reports whether s is a TON address.
func ValidAddress(s string) bool { _, err := ParseAddress(s); return err == nil }

// CommentPayloadBase64 builds the TON Connect "payload" of a plain text comment: a BOC with one cell
// (32 zero bits, then the UTF-8 text), base64 encoded.
func CommentPayloadBase64(comment string) (string, error) {
	data := append([]byte{0, 0, 0, 0}, []byte(comment)...)
	if len(data) > 127 {
		return "", errors.New("ton: comment too long")
	}
	cell := append([]byte{0x00, byte(len(data) * 2)}, data...) // d1 = 0 refs, d2 = 2*len for whole bytes
	boc := []byte{0xb5, 0xee, 0x9c, 0x72, 0x01, 0x01, 0x01, 0x01, 0x00, byte(len(cell)), 0x00}
	boc = append(boc, cell...)
	return base64.StdEncoding.EncodeToString(boc), nil
}

// NanoFor converts coins at a rate (coins per 1 TON) to nanoTON, rounded down: coins / rate * 1e9.
func NanoFor(coins int64, coinsPerTon float64) int64 {
	if coinsPerTon <= 0 || coins <= 0 {
		return 0
	}
	// exact with big numbers: coins * 1e9 / coinsPerTon, rate kept to 6 decimals
	rate := new(big.Float).SetPrec(200).SetFloat64(coinsPerTon)
	n := new(big.Float).SetPrec(200).SetInt64(coins)
	n.Mul(n, new(big.Float).SetInt64(Nano))
	n.Quo(n, rate)
	i, _ := n.Int(nil)
	return i.Int64()
}

// NanoOfTon converts a TON amount with decimals to nanoTON (rounded to the nearest).
func NanoOfTon(ton float64) int64 {
	v, _ := new(big.Float).Mul(big.NewFloat(ton), big.NewFloat(float64(Nano))).Int64()
	return v + 0
}

// TonString formats nanoTON as a decimal TON string without losing precision.
func TonString(nano int64) string {
	neg := nano < 0
	if neg {
		nano = -nano
	}
	s := fmt.Sprintf("%d.%09d", nano/Nano, nano%Nano)
	s = strings.TrimRight(s, "0")
	s = strings.TrimSuffix(s, ".")
	if s == "" {
		s = "0"
	}
	if neg {
		s = "-" + s
	}
	return s
}

// ---- chain reader

type OutMsg struct {
	Destination string
	Value       int64
	Comment     string
}

type Tx struct {
	Hash     string
	LT       int64
	Time     time.Time
	Source   string
	Value    int64 // incoming nanoTON
	Comment  string
	Outgoing []OutMsg
}

// Chain reads the latest transactions of an address.
type Chain interface {
	Transactions(ctx context.Context, address string, limit int) ([]Tx, error)
}

// Toncenter reads through the toncenter v2 HTTP API.
type Toncenter struct {
	base, key string
	http      *http.Client
}

func NewToncenter(baseURL, apiKey string) *Toncenter {
	return &Toncenter{base: strings.TrimRight(baseURL, "/"), key: apiKey, http: &http.Client{Timeout: 15 * time.Second}}
}

type tcMsg struct {
	Source      string `json:"source"`
	Destination string `json:"destination"`
	Value       string `json:"value"`
	Message     string `json:"message"`
	MsgData     struct {
		Type string `json:"@type"`
		Text string `json:"text"`
	} `json:"msg_data"`
}

func (m tcMsg) comment() string {
	if m.Message != "" {
		return m.Message
	}
	if m.MsgData.Type == "msg.dataText" && m.MsgData.Text != "" {
		if b, err := base64.StdEncoding.DecodeString(m.MsgData.Text); err == nil {
			return string(b)
		}
	}
	return ""
}

func (t *Toncenter) Transactions(ctx context.Context, address string, limit int) ([]Tx, error) {
	// archival: an ordinary node forgets old blocks, so a wallet whose last transaction is a few days old fails with "lt not in db"
	q := url.Values{"address": {address}, "limit": {strconv.Itoa(limit)}, "archival": {"true"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, t.base+"/getTransactions?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	if t.key != "" {
		req.Header.Set("X-API-Key", t.key)
	}
	res, err := t.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	var out struct {
		OK     bool   `json:"ok"`
		Error  string `json:"error"`
		Result []struct {
			Utime int64 `json:"utime"`
			TxID  struct {
				LT   string `json:"lt"`
				Hash string `json:"hash"`
			} `json:"transaction_id"`
			InMsg   tcMsg   `json:"in_msg"`
			OutMsgs []tcMsg `json:"out_msgs"`
		} `json:"result"`
	}
	if err := json.Unmarshal(b, &out); err != nil || !out.OK {
		return nil, fmt.Errorf("toncenter: %s (%d)", out.Error, res.StatusCode)
	}
	txs := make([]Tx, 0, len(out.Result))
	for _, r := range out.Result {
		lt, _ := strconv.ParseInt(r.TxID.LT, 10, 64)
		val, _ := strconv.ParseInt(r.InMsg.Value, 10, 64)
		x := Tx{Hash: r.TxID.Hash, LT: lt, Time: time.Unix(r.Utime, 0), Source: r.InMsg.Source, Value: val, Comment: r.InMsg.comment()}
		for _, o := range r.OutMsgs {
			v, _ := strconv.ParseInt(o.Value, 10, 64)
			x.Outgoing = append(x.Outgoing, OutMsg{Destination: o.Destination, Value: v, Comment: o.comment()})
		}
		txs = append(txs, x)
	}
	return txs, nil
}

// FakeChain is a scripted chain for tests.
type FakeChain struct {
	Txs []Tx
	Err error
}

func (f *FakeChain) Transactions(ctx context.Context, address string, limit int) ([]Tx, error) {
	if f.Err != nil {
		return nil, f.Err
	}
	return f.Txs, nil
}
