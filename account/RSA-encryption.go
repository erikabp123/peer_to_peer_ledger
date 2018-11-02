package account

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/gob"
	"fmt"
	"io"
	"io/ioutil"
	"math/big"
	"os"
	"time"
)

func testRSASignature() {
	_, sk := KeyGen(2000)
	msg := big.NewInt(938912312313123000)
	hashedMsg := Hash(msg)
	sigStart := time.Now()
	Sign(hashedMsg, sk)
	sigElapsed := time.Since(sigStart)
	fmt.Println("Time to generate RSA sig:", sigElapsed.Nanoseconds(), "ns")
}

func testHashing() {
	fileBytes, err := ioutil.ReadFile("big_boi_file_hashing_test.txt")
	if err != nil {
		panic(err)
	}
	start := time.Now()
	hashed := sha256.New()
	hashed.Write(fileBytes)
	hashedmsg := new(big.Int)
	hashedmsg.SetBytes(hashed.Sum(nil))
	elapsed := time.Since(start)
	fmt.Println("Hashing", len(fileBytes), "bytes took:", elapsed.Nanoseconds(), "ns")
}

func convertSecretKeyToByteSlice(key SecretKey) []byte {
	var buffer bytes.Buffer
	enc := gob.NewEncoder(&buffer)
	enc.Encode(key)
	return buffer.Bytes()
}

func convertByteSliceToSecretKey(slice []byte) *SecretKey {
	var key *SecretKey
	reader := bytes.NewReader(slice)
	dec := gob.NewDecoder(reader)
	err := dec.Decode(&key)
	if err != nil {
		panic(err)
	}
	return key
}

type PublicKey struct {
	E *big.Int
	N *big.Int
}

type SecretKey struct {
	N *big.Int
	D *big.Int
}

func KeyGen(k int) (*PublicKey, *SecretKey) {
	e := big.NewInt(3)
	one := big.NewInt(1)
	var (
		foundp bool
		foundq bool
	)
	p := generatePrime(k / 2)
	for !foundp {
		mod := new(big.Int)
		sub := new(big.Int)
		mod = mod.Mod(sub.Sub(p, big.NewInt(1)), e)
		if mod.Cmp(one) == 0 {
			foundp = true
		} else {
			p = generatePrime(k / 2)
		}
	}
	q := generatePrime(k / 2)
	for !foundq {
		mod := new(big.Int)
		sub := new(big.Int)
		mod = mod.Mod(sub.Sub(q, big.NewInt(1)), e)
		if mod.Cmp(one) == 0 {
			foundq = true
		} else {
			q = generatePrime(k / 2)
		}
	}

	n := new(big.Int)
	n = n.Mul(p, q)
	l := new(big.Int)
	l = l.Mul(p.Sub(p, one), q.Sub(q, one))
	d := e.ModInverse(e, l)
	pk := new(PublicKey)
	pk.N = n
	pk.E = big.NewInt(3)
	sk := new(SecretKey)
	sk.N = n
	sk.D = d
	return pk, sk
}

func Verify(sig *big.Int, msg *big.Int, pk *PublicKey) bool {
	originalMsg := Encrypt(sig, pk)
	if originalMsg.Cmp(Hash(msg)) == 0 {
		return true
	}
	return false
}

func Sign(hash *big.Int, key *SecretKey) *big.Int {
	return Decrypt(hash, key)
}

func Hash(msg *big.Int) *big.Int {
	hashed := sha256.New()
	hashed.Write(msg.Bytes())
	hashedmsg := new(big.Int)
	hashedmsg.SetBytes(hashed.Sum(nil))
	return hashedmsg
}

func Encrypt(msg *big.Int, key *PublicKey) *big.Int {
	val := msg
	c := val.Exp(val, key.E, key.N)
	return c
}

func Decrypt(cipher *big.Int, key *SecretKey) *big.Int {
	m := new(big.Int)
	m.Exp(cipher, key.D, key.N)
	return m
}

func generatePrime(k int) *big.Int {
	p, err := rand.Prime(rand.Reader, k)
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
	return p
}

func EncryptToFile(fileName string, plaintext []byte, keyAES []byte) bool {
	block, err := aes.NewCipher(keyAES)
	if err != nil {
		panic(err)
	}

	ciphertext := make([]byte, aes.BlockSize+len(plaintext))
	iv := ciphertext[:aes.BlockSize]
	if _, err := io.ReadFull(rand.Reader, iv); err != nil {
		panic(err)
	}
	stream := cipher.NewCTR(block, iv)
	stream.XORKeyStream(ciphertext[aes.BlockSize:], plaintext)

	return writeByteSliceToFile(ciphertext, fileName)
}

func writeByteSliceToFile(bytes []byte, fileName string) bool {
	err := ioutil.WriteFile(fileName, bytes, 0644)
	if err != nil {
		return false
	}
	return true
}

func DecryptFromFile(fileName string, keyAES []byte) []byte {
	ciphertext, err := ioutil.ReadFile(fileName)

	block, err := aes.NewCipher(keyAES)
	if err != nil {
		panic(err)
	}

	iv := ciphertext[:aes.BlockSize]
	ciphertext = ciphertext[aes.BlockSize:]
	stream := cipher.NewCTR(block, iv)
	stream.XORKeyStream(ciphertext, ciphertext)

	return ciphertext
}
