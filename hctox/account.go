package hctox

import (
	"encoding/binary"
	"encoding/hex"
	"io"
	"strings"

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

func GenerateAccount(randReader io.Reader, nospam uint32) (*Account, error) {
	var scalar [32]byte
	if _, err := io.ReadFull(randReader, scalar[:]); err != nil {
		return nil, err
	}

	a := Account{
		Secret: strings.ToUpper(hex.EncodeToString(scalar[:])),
		Nospam: nospam,
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

	binary.BigEndian.PutUint32(toxid[32:], nospam)

	checksum := toxid[36:]
	for i := 0; i < 36; i++ {
		checksum[i%2] ^= toxid[i]
	}

	return toxid
}
