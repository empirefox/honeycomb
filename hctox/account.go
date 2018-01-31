package hctox

import (
	"encoding/hex"
	"io"
	"math/rand"
	"strings"
	"unsafe"

	"golang.org/x/crypto/curve25519"
)

type Account struct {
	ToxID  string
	Secret string
	Nospam uint32
}

func NewAccount(secret string, nospam uint32) (*Account, error) {
	toxid, err := ToxID(secret, nospam)
	if err != nil {
		return nil, err
	}
	return &Account{
		ToxID:  toxid,
		Secret: secret,
		Nospam: nospam,
	}, nil
}

func GenerateAccount(randReader io.Reader) (*Account, error) {
	var scalar [32]byte
	if _, err := io.ReadFull(randReader, scalar[:]); err != nil {
		return nil, err
	}

	a := Account{
		Secret: strings.ToUpper(hex.EncodeToString(scalar[:])),
		Nospam: rand.Uint32(),
	}
	toxid := toxID(&scalar, a.Nospam)
	a.ToxID = strings.ToUpper(hex.EncodeToString(toxid))
	return &a, nil
}

func ToxID(secret string, nospam uint32) (string, error) {
	var scalar [32]byte

	sk, err := hex.DecodeString(strings.ToLower(secret))
	if err != nil {
		return "", err
	}

	if copy(scalar[:], sk) != 32 {
		return "", ErrKeyLen
	}

	toxid := toxID(&scalar, nospam)
	return strings.ToUpper(hex.EncodeToString(toxid)), nil
}

func toxID(secret *[32]byte, nospam uint32) (toxid []byte) {
	toxid = make([]byte, 32+4+2)

	var public [32]byte
	curve25519.ScalarBaseMult(&public, secret)
	copy(toxid[:32], public[:])

	b := (*[4]byte)(unsafe.Pointer(&nospam))
	toxid[32], toxid[33], toxid[34], toxid[35] = b[3], b[2], b[1], b[0]

	checksum := toxid[36:]
	for i := 0; i < 36; i++ {
		checksum[i%2] ^= toxid[i]
	}

	return toxid
}
