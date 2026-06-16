package main

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
)

type result struct {
	PublicKey      string `json:"public_key"`
	CheckpointHash string `json:"checkpoint_hash"`
	Signature      string `json:"signature"`
}

func main() {
	if len(os.Args) != 7 {
		fatalf("usage: go run docker/scripts/hysteresis-sign.go <domain-id> <height> <block-hash-b64> <app-hash-b64> <block-time-unix> <seed>")
	}

	domainID := os.Args[1]
	height, err := strconv.ParseUint(os.Args[2], 10, 64)
	if err != nil {
		fatalf("invalid height: %v", err)
	}
	blockHash, err := base64.StdEncoding.DecodeString(os.Args[3])
	if err != nil {
		fatalf("invalid block hash base64: %v", err)
	}
	appHash, err := base64.StdEncoding.DecodeString(os.Args[4])
	if err != nil {
		fatalf("invalid app hash base64: %v", err)
	}
	blockTimeUnix, err := strconv.ParseInt(os.Args[5], 10, 64)
	if err != nil {
		fatalf("invalid block time: %v", err)
	}

	seed := sha256.Sum256([]byte(os.Args[6]))
	privateKey := ed25519.NewKeyFromSeed(seed[:])
	publicKey := privateKey.Public().(ed25519.PublicKey)
	checkpointHash := computeCheckpointHash(domainID, height, blockHash, appHash, nil, nil, blockTimeUnix)
	signature := ed25519.Sign(privateKey, checkpointHash)

	enc := json.NewEncoder(os.Stdout)
	if err := enc.Encode(result{
		PublicKey:      base64.StdEncoding.EncodeToString(publicKey),
		CheckpointHash: base64.StdEncoding.EncodeToString(checkpointHash),
		Signature:      base64.StdEncoding.EncodeToString(signature),
	}); err != nil {
		fatalf("encode result: %v", err)
	}
}

func computeCheckpointHash(domainID string, height uint64, blockHash, appHash, validatorSetHash, previousCheckpointHash []byte, blockTimeUnix int64) []byte {
	h := sha256.New()
	writeBytes(h, []byte(domainID))
	writeUint64(h, height)
	writeBytes(h, blockHash)
	writeBytes(h, appHash)
	writeBytes(h, validatorSetHash)
	writeBytes(h, previousCheckpointHash)
	writeUint64(h, uint64(blockTimeUnix))
	return h.Sum(nil)
}

func writeBytes(h interface{ Write([]byte) (int, error) }, value []byte) {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(value)))
	_, _ = h.Write(length[:])
	_, _ = h.Write(value)
}

func writeUint64(h interface{ Write([]byte) (int, error) }, value uint64) {
	var bz [8]byte
	binary.BigEndian.PutUint64(bz[:], value)
	_, _ = h.Write(bz[:])
}

func fatalf(format string, args ...any) {
	_, _ = fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
