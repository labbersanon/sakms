package usenet

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"fmt"
)

// Claude 2026-10-20: empty-password 7zAES on encoded headers.
// Reason: live ngPost nid=0x17 sz=58 is AES+LZMA (`06f10701`), not plain
//   LZMA. Indexers that rename hash.7z still see CRC after header decrypt
//   when the poster used -mhe with an empty password. A non-empty password
//   stays a miss (no brute force).
// Troubleshooting: 7z encoded lzma err=aes after this → real password or
//   NumCyclesPower > 20. Review if: ngPost starts using a non-empty -p.
// Related files: sevenz.go unpack7zFolder

const max7zAESCycles = 20

// sevenZAESPassword is UTF-16LE of the 7z header password. Production
// leaves it nil (empty). Tests set it for -mhe=on -p archives.
var sevenZAESPassword []byte

func decrypt7zAESEmpty(props, packed []byte) ([]byte, error) {
	return decrypt7zAES(props, packed, sevenZAESPassword)
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
