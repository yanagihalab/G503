package cmd

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	cmtcfg "github.com/cometbft/cometbft/config"
	cmtcrypto "github.com/cometbft/cometbft/crypto"
	"github.com/cometbft/cometbft/p2p"
	"github.com/cometbft/cometbft/privval"
	cmttypes "github.com/cometbft/cometbft/types"
	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	serverconfig "github.com/cosmos/cosmos-sdk/server/config"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/spf13/cobra"

	"github.com/yanagihalab/G503/app"
	sanctiontypes "github.com/yanagihalab/G503/x/sanction/types"
)

type demoKeys struct {
	ChainID      string `json:"chain_id"`
	Faucet       keyRef `json:"faucet"`
	Demo         keyRef `json:"demo"`
	Validator    keyRef `json:"validator"`
	Agent        keyRef `json:"agent"`
	Authority    keyRef `json:"authority"`
	ValidatorPub string `json:"validator_consensus_pubkey"`
}

type keyRef struct {
	Address       string `json:"address"`
	PrivateKeyHex string `json:"private_key_hex"`
	AccountNumber uint64 `json:"account_number"`
	Sequence      uint64 `json:"sequence"`
}

func initCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "init [moniker]",
		Short: "Initialize a local G503 node with demo genesis state",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			home, err := cmd.Flags().GetString(flagsHome)
			if err != nil {
				return err
			}
			chainID, err := cmd.Flags().GetString(flagsChainID)
			if err != nil {
				return err
			}
			if chainID == "" {
				return fmt.Errorf("--chain-id is required")
			}
			return initLocalNode(home, chainID, args[0])
		},
	}
	return cmd
}

func initLocalNode(home string, chainID string, moniker string) error {
	if err := os.MkdirAll(filepath.Join(home, "config"), 0o700); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(home, "data"), 0o700); err != nil {
		return err
	}
	cmtConfig := initCometBFTConfig()
	cmtConfig.SetRoot(home)
	cmtConfig.Moniker = moniker
	cmtcfg.WriteConfigFile(filepath.Join(home, "config", "config.toml"), cmtConfig)
	appConfig := serverconfig.DefaultConfig()
	appConfig.MinGasPrices = "0stake"
	serverconfig.WriteConfigFile(filepath.Join(home, "config", "app.toml"), appConfig)
	if _, err := p2p.LoadOrGenNodeKey(cmtConfig.NodeKeyFile()); err != nil {
		return err
	}
	filePV := privval.LoadOrGenFilePV(cmtConfig.PrivValidatorKeyFile(), cmtConfig.PrivValidatorStateFile())
	filePV.Save()

	faucetKey := secp256k1.GenPrivKey()
	demoKey := secp256k1.GenPrivKey()
	validatorKey := secp256k1.GenPrivKey()
	agentKey := secp256k1.GenPrivKey()
	authorityKey := secp256k1.GenPrivKey()
	faucetAddr := sdk.AccAddress(faucetKey.PubKey().Address()).String()
	demoAddr := sdk.AccAddress(demoKey.PubKey().Address()).String()
	validatorAddr := sdk.AccAddress(validatorKey.PubKey().Address()).String()
	agentAddr := sdk.AccAddress(agentKey.PubKey().Address()).String()
	authorityAddr := sdk.AccAddress(authorityKey.PubKey().Address()).String()

	encoding := app.MakeEncodingConfig()
	appState := app.DefaultGenesisState(encoding.Codec, app.DemoGenesisAccounts{
		FaucetAddress:    faucetAddr,
		DemoAddress:      demoAddr,
		ValidatorAddress: validatorAddr,
		AuthorityAddress: authorityAddr,
		Agent: &sanctiontypes.AgentInfo{
			AgentId:                  "validator-1",
			ValidatorOperatorAddress: sdk.ValAddress(sdk.AccAddress(validatorKey.PubKey().Address())).String(),
			SignerAddress:            agentAddr,
			PublicKey:                agentKey.PubKey().Bytes(),
			VotingPower:              1,
			Active:                   true,
			MetadataUri:              "local-demo",
		},
	})
	appStateJSON, err := json.Marshal(appState)
	if err != nil {
		return err
	}
	pubKey, err := filePV.GetPubKey()
	if err != nil {
		return err
	}
	genesis := &cmttypes.GenesisDoc{
		GenesisTime: time.Now().UTC(),
		ChainID:     chainID,
		Validators: []cmttypes.GenesisValidator{
			{
				Address: pubKey.Address(),
				PubKey:  pubKey,
				Power:   1,
				Name:    moniker,
			},
		},
		ConsensusParams: cmttypes.DefaultConsensusParams(),
		AppState:        appStateJSON,
	}
	if err := genesis.SaveAs(cmtConfig.GenesisFile()); err != nil {
		return err
	}
	keys := demoKeys{
		ChainID: chainID,
		Faucet:  keyRef{Address: faucetAddr, PrivateKeyHex: hex.EncodeToString(faucetKey.Key), AccountNumber: 0},
		Demo:    keyRef{Address: demoAddr, PrivateKeyHex: hex.EncodeToString(demoKey.Key), AccountNumber: 1},
		Validator: keyRef{
			Address:       validatorAddr,
			PrivateKeyHex: hex.EncodeToString(validatorKey.Key),
			AccountNumber: 2,
		},
		Agent:        keyRef{Address: agentAddr, PrivateKeyHex: hex.EncodeToString(agentKey.Key), AccountNumber: 3},
		Authority:    keyRef{Address: authorityAddr, PrivateKeyHex: hex.EncodeToString(authorityKey.Key), AccountNumber: 4},
		ValidatorPub: fmt.Sprintf("%X", cmtcrypto.AddressHash(pubKey.Bytes())),
	}
	keyBytes, err := json.MarshalIndent(keys, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(home, "config", "demo-keys.json"), keyBytes, 0o600); err != nil {
		return err
	}
	fmt.Printf("initialized G503 node at %s\n", home)
	fmt.Printf("chain-id: %s\n", chainID)
	fmt.Printf("demo keys: %s\n", filepath.Join(home, "config", "demo-keys.json"))
	return nil
}
