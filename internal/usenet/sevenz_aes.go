package usenet

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"strings"
	"unicode/utf16"
)

const max7zAESCycles = 20

// sevenZAESKeysFromStrings is UTF-16LE of each non-empty password. Never log
// the input; callers pass these into header decrypt only.
func sevenZAESKeysFromStrings(passwords []string) [][]byte {
	var keys [][]byte
	for _, pw := range passwords {
		pw = strings.TrimSpace(pw)
		if pw == "" {
			continue
		}
		keys = append(keys, utf16LEPassword(pw))
	}
	return keys
}

func utf16LEPassword(s string) []byte {
	u := utf16.Encode([]rune(s))
	out := make([]byte, 2*len(u))
	for i, w := range u {
		binary.LittleEndian.PutUint16(out[i*2:], w)
	}
	return out
}

func decrypt7zAES(props, packed, password []byte) ([]byte, error) {
	if len(props) < 2 {
		return nil, fmt.Errorf("aes props %d", len(props))
	}
	cycles := int(props[0] & 0x3f)
	if cycles > max7zAESCycles && cycles != 0x3f {
		return nil, fmt.Errorf("aes cycles %d", cycles)
	}
	ivSize := int((props[0]>>6)&1) + int(props[1]&0x0f)
	saltSize := int((props[0]>>7)&1) + int(props[1]>>4)
	if 2+saltSize+ivSize > len(props) {
		return nil, fmt.Errorf("aes prop len %d salt=%d iv=%d", len(props), saltSize, ivSize)
	}
	salt := props[2 : 2+saltSize]
	iv := make([]byte, 16)
	copy(iv, props[2+saltSize:2+saltSize+ivSize])
	key, err := sevenZAESKey(password, cycles, salt)
	if err != nil {
		return nil, err
	}
	if len(packed) < aes.BlockSize || len(packed)%aes.BlockSize != 0 {
		return nil, fmt.Errorf("aes packed %d", len(packed))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	out := make([]byte, len(packed))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(out, packed)
	return out, nil
}

func sevenZAESKey(password []byte, cycles int, salt []byte) ([]byte, error) {
	if cycles == 0x3f {
		key := make([]byte, 32)
		copy(key, salt)
		if len(salt) < 32 {
			copy(key[len(salt):], password)
		}
		return key, nil
	}
	if cycles < 0 || cycles > max7zAESCycles {
		return nil, fmt.Errorf("aes cycles %d", cycles)
	}
	h := sha256.New()
	var extra [8]byte
	rounds := uint64(1) << cycles
	for j := uint64(0); j < rounds; j++ {
		h.Write(salt)
		h.Write(password)
		h.Write(extra[:])
		for k := 0; k < 8; k++ {
			extra[k]++
			if extra[k] != 0 {
				break
			}
		}
	}
	return h.Sum(nil), nil
}
